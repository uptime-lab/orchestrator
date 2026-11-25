package results

import (
	"time"

	entitlement_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/entitlement"
)

// EntitlementResult is the subscription card: what the install runs under
// and what that means for the next admission. It carries the signed terms
// and never the file itself — the file is a bearer value for the cap.
//
// Two shapes. The account that operates the install (InstanceOperator) gets
// the terms and the rejections. Every other account's super_admin gets the
// SUMMARY: state, caps, the caller's own admitted count, expiry and grace —
// never the customer id, the kind, the note, the source or the rejections,
// which describe the operator's contract and the files other accounts
// offered, and never the install-wide admitted count, which tells a tenant
// how many devices the whole service manages.
//
// A third shape (Mode "account_limit") answers an account bound by the
// per-account device limit: its own limit, plan, end date and admitted
// count, and the contact — and NOTHING of the install's entitlement (no
// state, caps, expiry, grace or subscription link): those are the
// operator's licence, and shown to a tenant they read as the tenant's own.
type EntitlementResult struct {
	// InstanceOperator: the caller's account operates this install, so this
	// is the full card and the caller may apply a file.
	InstanceOperator bool `json:"instance_operator"`
	// Mode: instance (the install's entitlement, both shapes above) |
	// account_limit (the caller's own per-account limit).
	Mode string `json:"mode"`
	// State: free | active | grace | lapsed | rejected. Instance mode only.
	State string `json:"state,omitempty"`
	// Source: none | file | database. Operator only.
	Source string `json:"source,omitempty"`
	// FreeDeviceCap is what applies with no entitlement. Instance mode only.
	FreeDeviceCap *int `json:"free_device_cap,omitempty"`
	// EffectiveDeviceCap is what admission enforces now; 0 = unlimited.
	// Instance mode only.
	EffectiveDeviceCap *int `json:"effective_device_cap,omitempty"`
	// DeviceCapBelowFreeFloor: the file in force signs a positive cap below
	// the free cap, so the free cap (the floor) is what EffectiveDeviceCap
	// carries. Operator only, like the signed DeviceCap it explains.
	DeviceCapBelowFreeFloor bool `json:"device_cap_below_free_floor,omitempty"`
	// AdmittedDevices is the install's admitted count, every account
	// counted; -1 when it could not be read. Operator only: absent from the
	// summary.
	AdmittedDevices *int64 `json:"admitted_devices,omitempty"`
	// AccountDevices is the caller's own account's admitted count; -1 when
	// it could not be read. Both shapes. Admitted, like the cap: a node
	// pending approval, awaiting its first connect or rejected is not in it.
	AccountDevices int64 `json:"account_devices"`
	// SubscriptionURL is where to go for a file. Instance mode only.
	SubscriptionURL string `json:"subscription_url,omitempty"`

	// The per-account limit, account_limit mode only.
	// AccountDeviceLimit is the account's limit now: 0 = no limit, -1 = it
	// could not be read.
	AccountDeviceLimit *int `json:"account_device_limit,omitempty"`
	// AccountPlan: free (the install's default limit) | subscription (an
	// operator's override in force). Absent when the limit could not be read.
	AccountPlan string `json:"account_plan,omitempty"`
	// AccountPlanUntil is when a dated subscription ends; absent for the free
	// plan and for a subscription with no end.
	AccountPlanUntil *time.Time `json:"account_plan_until,omitempty"`
	// AccountLimitContact is where to write for more devices; absent when the
	// install configures none.
	AccountLimitContact string `json:"account_limit_contact,omitempty"`

	// The file in force, absent when none.
	CustomerID string     `json:"customer_id,omitempty"`
	Kind       string     `json:"kind,omitempty"`
	DeviceCap  *int       `json:"device_cap,omitempty"`
	IssuedAt   *time.Time `json:"issued_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	GraceDays  *int       `json:"grace_days,omitempty"`
	GraceUntil *time.Time `json:"grace_until,omitempty"`
	Note       string     `json:"note,omitempty"`
	AppliedAt  *time.Time `json:"applied_at,omitempty"`

	// Rejections names every offered file that did not verify, by source.
	Rejections []EntitlementRejectionResult `json:"rejections,omitempty"`

	// Applied describes the file the request pasted: present on the PUT
	// answer only, operator only. The terms above are the file IN FORCE,
	// which is another file when the pasted one was issued before a file
	// still active or in grace, or has already lapsed.
	Applied *EntitlementAppliedResult `json:"applied,omitempty"`
}

// EntitlementAppliedResult is the pasted file's own terms and standing.
type EntitlementAppliedResult struct {
	// InForce: the pasted file is the one the install now runs under.
	InForce   bool      `json:"in_force"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// DeviceCap is the signed cap; 0 = unlimited.
	DeviceCap int `json:"device_cap"`
	// State is the pasted file's own phase: active | grace | lapsed.
	State string `json:"state"`
}

// EntitlementRejectionResult is one offered file that did not verify.
type EntitlementRejectionResult struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// Modes of EntitlementResult.
const (
	EntitlementModeInstance     = "instance"
	EntitlementModeAccountLimit = "account_limit"
)

// NewEntitlementResult maps the snapshot: the full card for the instance
// operator, the account's own limit for an account the per-account limit
// binds, the summary for anyone else.
func NewEntitlementResult(s entitlement_service.Snapshot, instanceOperator bool) EntitlementResult {
	if s.AccountLimit != nil && !instanceOperator {
		return newAccountLimitResult(s)
	}
	freeCap, effectiveCap := s.FreeCap, s.EffectiveCap
	out := EntitlementResult{
		InstanceOperator:   instanceOperator,
		Mode:               EntitlementModeInstance,
		State:              string(s.State),
		FreeDeviceCap:      &freeCap,
		EffectiveDeviceCap: &effectiveCap,
		AccountDevices:     s.AccountAdmittedCount,
		SubscriptionURL:    s.SubscriptionURL,
	}
	if !instanceOperator {
		if s.Document != nil {
			expires, graceUntil := s.Document.ExpiresAt, s.GraceUntil
			out.ExpiresAt = &expires
			out.GraceUntil = &graceUntil
		}
		return out
	}
	admitted := s.AdmittedCount
	out.AdmittedDevices = &admitted
	out.Source = string(s.Source)
	if s.Document != nil {
		d := s.Document
		cap, grace := d.DeviceCap, d.GraceDays
		issued, expires, graceUntil := d.IssuedAt, d.ExpiresAt, s.GraceUntil
		out.CustomerID = d.CustomerID
		out.Kind = string(d.Kind)
		out.DeviceCap = &cap
		out.DeviceCapBelowFreeFloor = s.SignedCapBelowFloor
		out.IssuedAt = &issued
		out.ExpiresAt = &expires
		out.GraceDays = &grace
		out.GraceUntil = &graceUntil
		out.Note = d.Note
		if !s.AppliedAt.IsZero() {
			applied := s.AppliedAt
			out.AppliedAt = &applied
		}
	}
	if a := s.Applied; a != nil && a.Document != nil {
		out.Applied = &EntitlementAppliedResult{
			InForce:   a.InForce,
			IssuedAt:  a.Document.IssuedAt,
			ExpiresAt: a.Document.ExpiresAt,
			DeviceCap: a.Document.DeviceCap,
			State:     string(a.State),
		}
	}
	for _, r := range s.Rejections {
		out.Rejections = append(out.Rejections, EntitlementRejectionResult{Source: string(r.Source), Reason: r.Reason})
	}
	return out
}

// newAccountLimitResult is the account_limit shape: built from the
// account's own view and count alone, so no field of the install's
// entitlement can reach it.
func newAccountLimitResult(s entitlement_service.Snapshot) EntitlementResult {
	v := s.AccountLimit
	limit := -1
	out := EntitlementResult{
		Mode:                EntitlementModeAccountLimit,
		AccountDevices:      s.AccountAdmittedCount,
		AccountLimitContact: v.Limit.Contact,
	}
	if v.Known {
		limit = v.Limit.Limit
		out.AccountPlan = string(v.Limit.Plan)
		if v.Limit.Until != nil {
			until := *v.Limit.Until
			out.AccountPlanUntil = &until
		}
	}
	out.AccountDeviceLimit = &limit
	return out
}
