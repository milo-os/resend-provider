package webhook

import (
	"context"
	"fmt"
	"io"
	"net/http"

	svix "github.com/svix/svix-webhooks/go"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"go.miloapis.com/email-provider-resend/internal/resend"
	notificationmiloapiscomv1alpha1 "go.miloapis.com/milo/pkg/apis/notification/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Webhook struct {
	Handler  Handler
	Endpoint string
	svix     *svix.Webhook
}

type Request struct {
	EmailEvent   *resend.ParsedEvent
	ContactEvent *resend.ParsedContactEvent
}

type Response struct {
	HttpStatus int `json:"HttpStatus"`
}

type HandlerFunc func(context.Context, Request) Response

func (f HandlerFunc) Handle(ctx context.Context, req Request) Response {
	return f(ctx, req)
}

type Handler interface {
	Handle(context.Context, Request) Response
}

// SetupSvix sets svix client in the Webhook struct.
func (w *Webhook) SetupSvix(svixClient *svix.Webhook) {
	w.svix = svixClient
}

const (
	contactStatusProviderIDIndexKey = "contact-status-providerID"
)

// SetupIndexes sets up the required field indexes for webhook operations
func SetupIndexes(mgr ctrl.Manager) error {
	// Index ContactGroupMembership objects by .status.providerID so that the webhook handler can
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&notificationmiloapiscomv1alpha1.Contact{},
		contactStatusProviderIDIndexKey,
		func(rawObj client.Object) []string {
			contact := rawObj.(*notificationmiloapiscomv1alpha1.Contact)
			if contact.Status.ProviderID == "" {
				return nil
			}
			return []string{contact.Status.ProviderID}
		},
	); err != nil {
		return fmt.Errorf("failed to createcontact group membership  index for providerID: %w", err)
	}

	return nil
}

// SetupWithManager sets up the webhook with the Manager
func (w *Webhook) SetupWithManager(mgr ctrl.Manager) error {
	hookServer := mgr.GetWebhookServer()
	hookServer.Register(w.Endpoint, w)

	return nil
}

func (wh *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context()).WithName("resend-http-webhook")
	log.Info("Handling request", "method", r.Method, "remoteAddr", r.RemoteAddr)

	// panic recovery
	defer func() {
		if r := recover(); r != nil {
			log.Error(nil, "Panic in webhook handler", "panic", r)
			wh.writeResponse(w, InternalServerErrorResponse())
		}
	}()

	if r.Method != http.MethodPost {
		log.Error(nil, "Method not allowed", "method", r.Method)
		w.Header().Set("Allow", http.MethodPost)
		wh.writeResponse(w, MethodNotAllowedResponse())
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error(err, "Failed to read request body")
		wh.writeResponse(w, InternalServerErrorResponse())
		return
	}
	defer func() {
		if err := r.Body.Close(); err != nil {
			log.Error(err, "Failed to close request body")
		}
	}()

	// Verify the signature of the request using svix
	if err := wh.svix.Verify(body, r.Header); err != nil {
		log.Error(err, "Failed to verify request")
		wh.writeResponse(w, UnauthorizedResponse())
		return
	}

	// Try to parse as email event first
	emailEvent, emailErr := resend.ParseEmailEvent(body)
	if emailErr == nil {
		response := wh.Handler.Handle(r.Context(), Request{
			EmailEvent: emailEvent,
		})
		wh.writeResponse(w, response)
		return
	}

	// If email parsing fails, try to parse as contact event
	contactEvent, contactErr := resend.ParseContactEvent(body)
	if contactErr == nil {
		response := wh.Handler.Handle(r.Context(), Request{
			ContactEvent: contactEvent,
		})
		wh.writeResponse(w, response)
		return
	}

	// If both parsing attempts fail, return bad request
	log.Error(emailErr, "Failed to parse email event")
	log.Error(contactErr, "Failed to parse contact event")
	wh.writeResponse(w, BadRequestResponse())
}

func (wh *Webhook) writeResponse(w http.ResponseWriter, response Response) {
	w.WriteHeader(response.HttpStatus)
}
