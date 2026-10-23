# NixOS module for calbooker (docs/deployment.md): one container on the host
# network, bound to loopback, next to the Hydra and provider containers of the
# goweb module (services.goweb), plus an optional Cloudflare Tunnel route and
# PostgreSQL role. Secrets never enter the Nix store: the container reads an
# environment file at run time (for example a sops-nix secret path).
{ config, lib, ... }:

let
  cfg = config.services.calbooker;
  inherit (lib) mkOption mkEnableOption mkIf types;

  backend = config.virtualisation.oci-containers.backend;
  unit = "${backend}-calbooker.service";

  hostOf = url: lib.head (lib.splitString "/" (lib.removePrefix "https://" url));

  postgresUnits = if config.systemd.targets ? postgresql then [ "postgresql.target" ] else [ "postgresql.service" ];

  # Defaults follow the goweb module when it is imported on the same host.
  goweb = config.services.goweb or { };
in
{
  options.services.calbooker = {
    enable = mkEnableOption "calbooker";

    image = mkOption {
      type = types.str;
      example = "ghcr.io/tewecske/calbooker:0123456789abcdef0123456789abcdef01234567";
      description = "calbooker image. Pin a commit tag.";
    };

    publicUrl = mkOption {
      type = types.str;
      example = "https://calbooker.example.com";
      description = "calbooker's public URL (https, no trailing slash).";
    };

    port = mkOption {
      type = types.port;
      default = 28090;
      description = "Loopback port calbooker listens on.";
    };

    issuerUrl = mkOption {
      type = types.str;
      default = goweb.issuerUrl or "";
      defaultText = lib.literalExpression "config.services.goweb.issuerUrl";
      example = "https://sso.example.com";
      description = "Hydra's public URL, the OpenID Connect issuer.";
    };

    providerUrl = mkOption {
      type = types.str;
      default = goweb.providerUrl or "";
      defaultText = lib.literalExpression "config.services.goweb.providerUrl";
      example = "https://auth.example.com";
      description = "The goweb provider's public URL; calbooker links to its account page.";
    };

    mailFrom = mkOption {
      type = types.str;
      default = goweb.mailFrom or "";
      defaultText = lib.literalExpression "config.services.goweb.mailFrom";
      example = "no-reply@example.com";
      description = "Sender address on a domain verified in Resend.";
    };

    sessionLifetime = mkOption {
      type = types.str;
      default = "8h";
      description = "Session lifetime. Short, because a session that missed a back-channel logout stays valid until it expires.";
    };

    environmentFile = mkOption {
      type = types.path;
      example = "/run/secrets/calbooker-env";
      description = "File with CALBOOKER_DATABASE_URL, CALBOOKER_SESSION_SECRET, CALBOOKER_RESEND_API_KEY, CALBOOKER_SSO_CLIENT_ID, and CALBOOKER_SSO_CLIENT_SECRET.";
    };

    extraEnvironment = mkOption {
      type = types.attrsOf types.str;
      default = { };
      description = "Extra non-secret environment variables for the container.";
    };

    registryLogin = mkOption {
      default = null;
      description = "Credentials for pulling a private image from ghcr.io.";
      type = types.nullOr (types.submodule {
        options = {
          username = mkOption { type = types.str; };
          passwordFile = mkOption { type = types.path; };
        };
      });
    };

    database = {
      manage = mkEnableOption "creating the calbooker PostgreSQL role and database and setting its password";
      passwordFile = mkOption {
        type = types.nullOr types.path;
        default = null;
        description = "File with the calbooker role's password. Required when database.manage is on.";
      };
    };

    cloudflared.tunnelId = mkOption {
      type = types.nullOr types.str;
      default = null;
      example = "00000000-0000-0000-0000-000000000000";
      description = "Adds calbooker's host to this existing Cloudflare Tunnel (the one services.goweb.cloudflared configures).";
    };
  };

  config = mkIf cfg.enable (lib.mkMerge [
    {
      assertions = [
        {
          assertion = lib.all (url: lib.hasPrefix "https://" url) [ cfg.publicUrl cfg.issuerUrl cfg.providerUrl ];
          message = "services.calbooker: publicUrl, issuerUrl, and providerUrl must be https URLs.";
        }
        {
          assertion = cfg.mailFrom != "";
          message = "services.calbooker: set mailFrom.";
        }
        {
          assertion = !cfg.database.manage || (config.services.postgresql.enable && cfg.database.passwordFile != null);
          message = "services.calbooker.database.manage needs services.postgresql.enable and database.passwordFile.";
        }
      ];

      virtualisation.oci-containers.containers.calbooker = {
        image = cfg.image;
        environment = {
          CALBOOKER_ENV = "production";
          CALBOOKER_HTTP_ADDR = "127.0.0.1:${toString cfg.port}";
          CALBOOKER_PUBLIC_URL = cfg.publicUrl;
          CALBOOKER_SESSION_COOKIE_SECURE = "true";
          CALBOOKER_TRUSTED_PROXY = "127.0.0.1";
          CALBOOKER_SESSION_LIFETIME = cfg.sessionLifetime;
          CALBOOKER_SSO_ISSUER_URL = cfg.issuerUrl;
          CALBOOKER_SSO_ACCOUNT_URL = "${cfg.providerUrl}/en/account/settings";
          CALBOOKER_MAIL_FROM = cfg.mailFrom;
        } // cfg.extraEnvironment;
        environmentFiles = [ cfg.environmentFile ];
        extraOptions = [ "--network=host" "--read-only" "--cap-drop=ALL" "--security-opt=no-new-privileges" ];
      } // lib.optionalAttrs (cfg.registryLogin != null) {
        login = {
          registry = "ghcr.io";
          inherit (cfg.registryLogin) username passwordFile;
        };
      };
    }

    (mkIf (cfg.cloudflared.tunnelId != null) {
      services.cloudflared.tunnels.${cfg.cloudflared.tunnelId}.ingress.${hostOf cfg.publicUrl} = "http://127.0.0.1:${toString cfg.port}";
    })

    (mkIf cfg.database.manage {
      systemd.services.calbooker-db-init = {
        description = "Create the calbooker PostgreSQL role and database";
        wantedBy = [ "multi-user.target" ];
        requires = postgresUnits;
        after = postgresUnits;
        before = [ unit ];
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          User = "postgres";
          LoadCredential = [ "calbooker:${toString cfg.database.passwordFile}" ];
        };
        # Idempotent: creates what is missing, resets the role's password to the
        # file's value, and lets only the role connect to its database.
        script = ''
          ${config.services.postgresql.package}/bin/psql -X -v ON_ERROR_STOP=1 -d postgres <<'SQL'
          \set pw `cat "$CREDENTIALS_DIRECTORY/calbooker"`
          SELECT 'CREATE ROLE "calbooker" LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'calbooker') \gexec
          ALTER ROLE "calbooker" PASSWORD :'pw';
          SELECT 'CREATE DATABASE "calbooker" OWNER "calbooker"' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'calbooker') \gexec
          REVOKE CONNECT ON DATABASE "calbooker" FROM PUBLIC;
          SQL
        '';
      };
      systemd.services.${lib.removeSuffix ".service" unit} = {
        requires = [ "calbooker-db-init.service" ];
        after = [ "calbooker-db-init.service" ];
      };
    })
  ]);
}
