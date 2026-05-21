// Package audit publishes audit events to NATS and persists them via the store.
// Schema mirrors gateway/audit so a shared backend consumer can index both.
package audit

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

type AuditEvent struct {
	ID           string    `json:"id"`
	Timestamp    time.Time `json:"timestamp"`
	UserID       string    `json:"userId,omitempty"`
	Username     string    `json:"username"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId,omitempty"`
	ResourceName string    `json:"resourceName,omitempty"`
	Details      string    `json:"details,omitempty"`
	IPAddress    string    `json:"ipAddress,omitempty"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
	Source       string    `json:"source,omitempty"`
}

// Sink persists events locally (in addition to the slog + NATS publish).
type Sink interface {
	InsertAuditEvent(AuditEvent) error
}

type Auditor struct {
	nc     *nats.Conn
	sink   Sink
	logger *slog.Logger
}

func NewAuditor(nc *nats.Conn, sink Sink, logger *slog.Logger) *Auditor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Auditor{nc: nc, sink: sink, logger: logger}
}

func (a *Auditor) Log(event AuditEvent) {
	if event.ID == "" {
		event.ID = uuid.New().String()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.Source == "" {
		event.Source = "artifact-gateway"
	}
	if event.Status == "" {
		event.Status = "success"
	}

	a.logger.Info("audit event",
		"action", event.Action,
		"resourceType", event.ResourceType,
		"resourceName", event.ResourceName,
		"username", event.Username,
		"ip", event.IPAddress,
		"status", event.Status,
	)

	if a.sink != nil {
		if err := a.sink.InsertAuditEvent(event); err != nil {
			a.logger.Warn("failed to persist audit event", "error", err)
		}
	}

	if a.nc != nil && a.nc.IsConnected() {
		subject := "audit." + event.ResourceType + "." + event.Action
		if data, err := json.Marshal(event); err == nil {
			if err := a.nc.Publish(subject, data); err != nil {
				a.logger.Warn("failed to publish audit event", "error", err)
			}
		}
	}
}

// Convenience helpers — keep here so handlers don't construct AuditEvent inline.

func (a *Auditor) LogTokenMint(tokenID, licenseID, ip, status string) {
	a.Log(AuditEvent{
		Username:     tokenID,
		Action:       "mint",
		ResourceType: "customer-token",
		ResourceID:   licenseID,
		ResourceName: tokenID,
		IPAddress:    ip,
		Status:       status,
	})
}

func (a *Auditor) LogPackagePull(tokenID, packagePath, ref, ip string) {
	a.Log(AuditEvent{
		Username:     tokenID,
		Action:       "pull",
		ResourceType: "package",
		ResourceName: packagePath,
		Details:      "ref=" + ref,
		IPAddress:    ip,
	})
}

func (a *Auditor) LogAdminLogin(user, method, ip, status string) {
	a.Log(AuditEvent{
		Username:     user,
		Action:       "login",
		ResourceType: "admin",
		Details:      "method=" + method,
		IPAddress:    ip,
		Status:       status,
	})
}

func (a *Auditor) LogResourceMutation(user, action, resourceType, resourceID, resourceName, ip string) {
	a.Log(AuditEvent{
		Username:     user,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		ResourceName: resourceName,
		IPAddress:    ip,
	})
}
