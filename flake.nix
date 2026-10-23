{
  description = "calbooker: NixOS module that runs calbooker as a container next to the goweb single sign-on stack";

  outputs = { self }: {
    nixosModules.default = import ./nix/module.nix;
    nixosModules.calbooker = self.nixosModules.default;
  };
}
