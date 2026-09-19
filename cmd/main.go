// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"flag"
	"os"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/p3l1/pangolin-gateway/internal/controller"
	"github.com/p3l1/pangolin-gateway/internal/manager"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
	"github.com/p3l1/pangolin-gateway/internal/version"
)

// APIKeyEnv holds the integration key. It is deliberately not a flag: flags are
// visible in the process list.
const APIKeyEnv = "PANGOLIN_API_KEY"

func main() {
	cfg := manager.Config{}
	flag.StringVar(&cfg.MetricsAddress, "metrics-bind-address", ":8080", "address the metrics endpoint binds to")
	flag.StringVar(&cfg.ProbeAddress, "health-probe-bind-address", ":8081", "address the probe endpoint binds to")
	flag.BoolVar(&cfg.LeaderElection, "leader-elect", false, "elect a leader before acting")

	var (
		endpoint = flag.String("pangolin-endpoint", "",
			"Integration API base URL including its version path, e.g. https://api.example.com/v1")
		org         = flag.String("pangolin-org", "", "Pangolin organisation id")
		defaultSite = flag.String("default-site", "",
			"site niceId used for routes without the "+"pangolin.p3l1.de/site"+" annotation")
		dryRun = flag.Bool("dry-run", false,
			"log every write to Pangolin instead of performing it")
		resync = flag.Duration("resync-interval", controller.DefaultResyncInterval,
			"how often to reconcile without a cluster event, repairing drift on the Pangolin side")
	)

	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	log := ctrl.Log.WithName("setup")
	log.Info("starting pangolin-gateway", "version", version.String())

	// Every target needs a site, and a blueprint entry without one is rejected
	// for the whole document — so this fails at startup rather than per route.
	if *defaultSite == "" {
		log.Error(nil, "--default-site is required")
		os.Exit(1)
	}

	client, err := pangolin.NewHTTPClient(pangolin.Options{
		Endpoint: *endpoint,
		OrgID:    *org,
		APIKey:   os.Getenv(APIKeyEnv),
	})
	if err != nil {
		log.Error(err, "invalid Pangolin configuration",
			"endpoint", *endpoint, "org", *org, "apiKeyEnv", APIKeyEnv)
		os.Exit(1)
	}

	var pangolinClient pangolin.Client = client
	if *dryRun {
		log.Info("dry run: no write will reach Pangolin")
		pangolinClient = pangolin.NewDryRun(client, ctrl.Log.WithName("dry-run"))
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), manager.ControllerOptions(cfg))
	if err != nil {
		log.Error(err, "unable to create manager")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "unable to register health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "unable to register readiness check")
		os.Exit(1)
	}

	publisher := &controller.Publisher{
		Client:         mgr.GetClient(),
		Pangolin:       pangolinClient,
		ControllerName: manager.ControllerName,
		DefaultSite:    *defaultSite,
		ResyncInterval: *resync,
		DryRun:         *dryRun,
	}
	if err := publisher.SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create controller", "controller", "pangolin-publisher")
		os.Exit(1)
	}

	log.Info("configuration",
		"endpoint", *endpoint,
		"org", *org,
		"defaultSite", *defaultSite,
		"dryRun", *dryRun,
		"resyncInterval", time.Duration(*resync).String(),
		"controllerName", manager.ControllerName)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited with error")
		os.Exit(1)
	}
}
