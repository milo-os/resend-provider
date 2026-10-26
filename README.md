# Complete Kubernetes operator implementation that integrates Resend email service with the Milo platform for automated email delivery

## Architecture

* **Controller Manager**: Reconciles Email CRDs by fetching templates, rendering content, and sending emails via Resend

* **Webhook Server**: Processes Resend delivery events (sent/delivered/bounced) and updates Email status

* **Email Provider Interface**: Abstracted email provider supporting multiple backends (currently Resend)

### Key Features

* **Template Rendering**: Dynamic email content using EmailTemplate CRDs with variable substitution

* **Idempotency**: Sends the Email UID as Resend's `Idempotency-Key`, so a retry within 24 hours doesn't send the email again. Contacts and segment memberships are looked up before they're created.

* **Status Tracking**: Kubernetes conditions for email delivery states (Pending/Delivered/Failed)

* **Event Correlation**: Indexes emails by providerID for efficient webhook event processing

* **Priority Support**: Configurable retry delays based on email priority

* **Security**: SVIX webhook signature verification for event authenticity

### Components

* Dual-mode deployment: controller manager + separate webhook server

* RBAC for Email, EmailTemplate, and User CRD access

* Metrics and health endpoints with TLS support

* Leader election for HA deployments

Provides reliable, observable email delivery for the Milo notification system.
