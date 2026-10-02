/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/spf13/cobra"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"go.miloapis.com/email-provider-resend/internal/config"
	"go.miloapis.com/email-provider-resend/internal/controller"
	"go.miloapis.com/email-provider-resend/internal/emailprovider"
)

// nolint:gocyclo
func createManagerCommand() *cobra.Command {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var emailApiKey, emailFrom, emailReplyTo string
	var lowPriorityEmailWait, normalPriorityEmailWait, highPriorityEmailWait time.Duration
	// Leader election configuration options
	var leaderElectionID, leaderElectionNamespace, leaderElectionResourceLock string
	var leaseDuration, renewDeadline, retryPeriod time.Duration

	cmd := &cobra.Command{
		Use:   "manager",
		Short: "Start the controller manager",
		Long:  "Start the Kubernetes controller manager for the email provider resend",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runManager(
				metricsAddr,
				metricsCertPath, metricsCertName, metricsCertKey,
				webhookCertPath, webhookCertName, webhookCertKey,
				enableLeaderElection,
				probeAddr,
				secureMetrics,
				enableHTTP2,
				emailApiKey, emailFrom, emailReplyTo,
				lowPriorityEmailWait, normalPriorityEmailWait, highPriorityEmailWait,
				leaderElectionID, leaderElectionNamespace, leaderElectionResourceLock,
				leaseDuration, renewDeadline, retryPeriod)
		},
	}

	cmd.Flags().StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	cmd.Flags().StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	cmd.Flags().BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	cmd.Flags().BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	cmd.Flags().StringVar(&webhookCertPath, "webhook-cert-path", "",
		"The directory that contains the webhook certificate.")
	cmd.Flags().StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	cmd.Flags().StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")

	cmd.Flags().StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	cmd.Flags().StringVar(&metricsCertName, "metrics-cert-name", "tls.crt",
		"The name of the metrics server certificate file.")
	cmd.Flags().StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	cmd.Flags().BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")

	// Email provider config
	emailApiKey = os.Getenv("RESEND_API_KEY") // *Required. The API key for the email provider.
	cmd.Flags().StringVar(&emailFrom, "email-from-address", "", "*Required. The from address for the email provider.")
	cmd.Flags().StringVar(&emailReplyTo, "email-reply-to-address", "",
		"*Required. The reply to address for the email provider.")

	// Email controller config
	cmd.Flags().DurationVar(&lowPriorityEmailWait, "wait-time-before-retry-low-priority-email", 30*time.Second,
		"*Not required. The wait time before retrying a low priority email.")
	cmd.Flags().DurationVar(&normalPriorityEmailWait, "wait-time-before-retry-normal-priority-email", 10*time.Second,
		"*Not required. The wait time before retrying a normal priority email.")
	cmd.Flags().DurationVar(&highPriorityEmailWait, "wait-time-before-retry-high-priority-email", 1*time.Second,
		"*Not required. The wait time before retrying a high priority email.")

	// Leader election configuration flags
	cmd.Flags().StringVar(&leaderElectionID, "leader-election-id", "1adf6d2b.resend.notification.miloapis.com",
		"The name of the resource that leader election will use for holding the leader lock.")
	cmd.Flags().StringVar(&leaderElectionNamespace, "leader-election-namespace", "",
		"Namespace to use for leader election. If empty, the controller will discover the namespace it is running in.")
	cmd.Flags().StringVar(&leaderElectionResourceLock, "leader-election-resource-lock", "leases",
		"The type of resource object that is used for locking during leader election. Supported options are 'leases', "+
			"'endpointsleases' and 'configmapsleases'.")
	cmd.Flags().DurationVar(&leaseDuration, "leader-election-lease-duration", 15*time.Second,
		"The duration that non-leader candidates will wait after observing a leadership renewal until attempting to "+
			"acquire leadership of a led but unrenewed leader slot.")
	cmd.Flags().DurationVar(&renewDeadline, "leader-election-renew-deadline", 10*time.Second,
		"The interval between attempts by the acting master to renew a leadership slot before it stops leading.")
	cmd.Flags().DurationVar(&retryPeriod, "leader-election-retry-period", 2*time.Second,
		"The duration the clients should wait between attempting acquisition and renewal of a leadership.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	setupLog.Info("starting email-provider-resend",
		"version", version,
		"gitCommit", gitCommit,
		"gitTreeState", gitTreeState,
		"buildDate", buildDate,
	)

	return cmd
}

func runManager(
	metricsAddr string,
	metricsCertPath, metricsCertName, metricsCertKey string,
	webhookCertPath, webhookCertName, webhookCertKey string,
	enableLeaderElection bool,
	probeAddr string,
	secureMetrics bool,
	enableHTTP2 bool,
	emailApiKey, emailFrom, emailReplyTo string,
	lowPriorityEmailWait, normalPriorityEmailWait, highPriorityEmailWait time.Duration,
	leaderElectionID, leaderElectionNamespace, leaderElectionResourceLock string,
	leaseDuration, renewDeadline, retryPeriod time.Duration,
) error {
	// Create and validate email provider config
	emailConfig, err := config.NewEmailProviderConfig(emailApiKey, emailFrom, emailReplyTo)
	if err != nil {
		setupLog.Error(err, "unable to create email provider config")
		return fmt.Errorf("unable to create email provider config: %w", err)
	}

	// Create and validate email controller config
	emailCtrlConfig, err := config.NewEmailControllerConfig(
		lowPriorityEmailWait, normalPriorityEmailWait, highPriorityEmailWait)
	if err != nil {
		setupLog.Error(err, "unable to create email controller config")
		return fmt.Errorf("unable to create email controller config: %w", err)
	}

	var tlsOpts []func(*tls.Config)

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Create watchers for metrics and webhooks certificates
	var metricsCertWatcher, webhookCertWatcher *certwatcher.CertWatcher

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		var err error
		webhookCertWatcher, err = certwatcher.New(
			filepath.Join(webhookCertPath, webhookCertName),
			filepath.Join(webhookCertPath, webhookCertKey),
		)
		if err != nil {
			setupLog.Error(err, "Failed to initialize webhook certificate watcher")
			return fmt.Errorf("failed to initialize webhook certificate watcher: %w", err)
		}

		webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
			config.GetCertificate = webhookCertWatcher.GetCertificate
		})
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: webhookTLSOpts,
	})

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(metricsCertPath, metricsCertName),
			filepath.Join(metricsCertPath, metricsCertKey),
		)
		if err != nil {
			setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
			return fmt.Errorf("failed to initialize metrics certificate watcher: %w", err)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	restCfg := ctrl.GetConfigOrDie()

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                        scheme,
		Metrics:                       metricsServerOptions,
		WebhookServer:                 webhookServer,
		HealthProbeBindAddress:        probeAddr,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionNamespace:       leaderElectionNamespace,
		LeaderElectionResourceLock:    leaderElectionResourceLock,
		LeaseDuration:                 &leaseDuration,
		RenewDeadline:                 &renewDeadline,
		RetryPeriod:                   &retryPeriod,
		LeaderElectionConfig:          leaderElectionRestConfig(restCfg),
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		return fmt.Errorf("unable to start manager: %w", err)
	}

	resendEmailProvider := emailprovider.NewResendEmailProvider(emailConfig.GetAPIKey())
	emailProviderService := emailprovider.NewService(resendEmailProvider, emailConfig.GetFrom(), emailConfig.GetReplyTo())

	// Setup email controller
	if err := (&controller.EmailController{
		Client:        mgr.GetClient(),
		EmailProvider: *emailProviderService,
		Config:        *emailCtrlConfig,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Email")
		return fmt.Errorf("unable to create controller: %w", err)
	}
	// +kubebuilder:scaffold:builder

	// Setup contact controller
	if err := (&controller.ContactController{
		Client:        mgr.GetClient(),
		EmailProvider: *emailProviderService,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Contact")
		return fmt.Errorf("unable to create controller: %w", err)
	}

	// Setup contact group controller
	if err := (&controller.ContactGroupController{
		Client:        mgr.GetClient(),
		EmailProvider: *emailProviderService,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ContactGroup")
		return fmt.Errorf("unable to create controller: %w", err)
	}

	// Setup contact group membership controller
	if err := (&controller.ContactGroupMembershipController{
		Client:        mgr.GetClient(),
		EmailProvider: *emailProviderService,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ContactGroupMembership")
		return fmt.Errorf("unable to create controller: %w", err)
	}

	// Setup contact group membership removal controller
	if err := (&controller.ContactGroupMembershipRemovalController{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ContactGroupMembershipRemoval")
		return fmt.Errorf("unable to create controller: %w", err)
	}

	if metricsCertWatcher != nil {
		setupLog.Info("Adding metrics certificate watcher to manager")
		if err := mgr.Add(metricsCertWatcher); err != nil {
			setupLog.Error(err, "unable to add metrics certificate watcher to manager")
			return fmt.Errorf("unable to add metrics certificate watcher to manager: %w", err)
		}
	}

	if webhookCertWatcher != nil {
		setupLog.Info("Adding webhook certificate watcher to manager")
		if err := mgr.Add(webhookCertWatcher); err != nil {
			setupLog.Error(err, "unable to add webhook certificate watcher to manager")
			return err
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		return fmt.Errorf("problem running manager: %w", err)
	}
	return nil
}

const (
	leaderElectionQPS   = 5
	leaderElectionBurst = 10
)

func leaderElectionRestConfig(base *rest.Config) *rest.Config {
	cfg := rest.CopyConfig(base)
	cfg.RateLimiter = nil
	cfg.QPS = leaderElectionQPS
	cfg.Burst = leaderElectionBurst
	cfg.Dial = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	return cfg
}
