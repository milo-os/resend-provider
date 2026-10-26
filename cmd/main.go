package main

import (
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	iammiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/iam/v1alpha1"
	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"go.miloapis.com/email-provider-resend/internal/logredact"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	version      = "dev"
	gitCommit    = "unknown"
	gitTreeState = "unknown"
	buildDate    = "unknown"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(iammiloapiscomv1alpha1.AddToScheme(scheme))
	utilruntime.Must(notificationmiloapiscomv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "email-provider-resend",
		Short: "Resend is the email provider for Milo",
		Long:  "A Kubernetes controller that manages email provider for Milo.",
	}

	rootCmd.AddCommand(createManagerCommand())
	rootCmd.AddCommand(createWebhookCommand())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// newLogger builds a zap logger that keeps email addresses out of its output.
// Both commands log through it: errors from Resend and the API server can
// quote a recipient's address back.
func newLogger(opts ...zap.Opts) logr.Logger {
	return logredact.Wrap(zap.New(opts...))
}
