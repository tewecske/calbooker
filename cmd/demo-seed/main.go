// Command demo-seed resets the local calbooker database and fills it with
// demo data (internal/demoseed). It is development tooling: it refuses
// production and non-local databases. Run it with make demo-seed.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/tewecske/calbooker/internal/config"
	"github.com/tewecske/calbooker/internal/demoseed"
	"github.com/tewecske/calbooker/internal/store"
	"github.com/tewecske/calbooker/migrations"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	providerURL := flag.String("provider", os.Getenv("CALBOOKER_BROWSER_PROVIDER_URL"), "provider origin (default CALBOOKER_BROWSER_PROVIDER_URL, else the origin of CALBOOKER_SSO_ACCOUNT_URL)")
	mailpitURL := flag.String("mailpit", envOr("DEMO_SEED_MAILPIT_URL", "http://localhost:8025"), "Mailpit origin; empty skips email confirmation")
	gowebURL := flag.String("goweb-database-url", os.Getenv("DEMO_SEED_GOWEB_DATABASE_URL"), "goweb database URL (default: CALBOOKER_DATABASE_URL with database goweb)")
	flag.Parse()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	databaseURL := cfg.DatabaseURL.Reveal()
	name, err := demoseed.CheckTarget(databaseURL, string(cfg.Environment))
	if err != nil {
		return err
	}
	if *providerURL == "" {
		parsed, err := url.Parse(cfg.SSOAccountURL)
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("set -provider or CALBOOKER_BROWSER_PROVIDER_URL")
		}
		*providerURL = parsed.Scheme + "://" + parsed.Host
	}
	if *gowebURL == "" {
		if *gowebURL, err = demoseed.GowebDatabaseURL(databaseURL); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("demo seed: WIPING database %q (all tables except the migration history), provider %s\n", name, *providerURL)

	gowebDB, err := store.Open(ctx, *gowebURL)
	if err != nil {
		return fmt.Errorf("open goweb database (read-only lookups of provider user IDs): %w", err)
	}
	defer func() { _ = gowebDB.Close() }()

	provider := &demoseed.Provider{BaseURL: *providerURL, MailpitURL: *mailpitURL}
	var created []string
	subjects, err := demoseed.EnsureAccounts(ctx, demoseed.Accounts(),
		func(ctx context.Context, emails []string) (map[string]string, error) {
			return demoseed.LookupSubjects(ctx, gowebDB, emails)
		},
		func(ctx context.Context, email string) error {
			if err := provider.SignUp(ctx, email, demoseed.Password); err != nil {
				return err
			}
			created = append(created, email)
			fmt.Printf("  signed up %s\n", email)
			return nil
		},
		func(format string, args ...any) { fmt.Printf("  "+format+"\n", args...) },
	)
	if err != nil {
		return err
	}
	confirmed := 0
	for _, email := range created {
		ok, err := provider.Confirm(ctx, email)
		if err != nil {
			fmt.Printf("  confirm %s: %v\n", email, err)
			continue
		}
		if ok {
			confirmed++
		}
	}
	if len(created) > 0 {
		fmt.Printf("  confirmed %d of %d new accounts through Mailpit (the local provider may send no mail)\n", confirmed, len(created))
	}

	openContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	db, err := store.Open(openContext, databaseURL)
	cancel()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	migrator, err := store.NewMigrator(db, migrations.FS)
	if err != nil {
		return err
	}
	if err := migrator.Apply(ctx); err != nil {
		return err
	}
	if err := demoseed.Seed(ctx, db, subjects, time.Now()); err != nil {
		return err
	}
	fmt.Printf("demo seed: done. Sign in with <group>-<role>-<n>@example.com / %s (for example bvsc-coach-1@example.com)\n", demoseed.Password)
	return nil
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
