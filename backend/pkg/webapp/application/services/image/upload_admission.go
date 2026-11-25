package image

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

// UploadCapacityError reports that no upload slot is free for this request.
// The request is fine and nothing about the record refuses it; the caller's
// next step is to wait, which is what 429 says (a 409 would send them to
// delete something). Raised BEFORE the body is read and before the record is
// claimed, so a refused request holds nothing. PerAccount says which bound
// was hit: the server-wide pool, or this account's share of it.
type UploadCapacityError struct {
	Limit      int
	PerAccount bool
	// StagingBytes is set when the refusal is the server-wide staging byte
	// budget rather than a slot count: every slot may be free and the disk
	// still cannot take this body on top of the ones in flight.
	StagingBytes bool
}

func (e *UploadCapacityError) Error() string {
	if e.StagingBytes {
		return "The server cannot stage another upload of this size right now. Wait for an upload to finish and try again."
	}
	if e.PerAccount {
		return fmt.Sprintf("Your account already has %d image uploads in progress, the most it may run at once. Wait for one to finish and try again.", e.Limit)
	}
	return fmt.Sprintf("All %d image upload slots on this server are busy. Wait for an upload to finish and try again.", e.Limit)
}

func (e *UploadCapacityError) HTTPStatus() int   { return http.StatusTooManyRequests }
func (e *UploadCapacityError) ErrorCode() string { return "upload_capacity" }

var _ results.OperatorFacingError = (*UploadCapacityError)(nil)

// ImageQuotaExceededError reports that the account's (or the instance's)
// stored-image byte ceiling has no room for this upload. It is a 409, not a
// 429: the caller's next step is to delete an image, not to wait. Typed so
// that BeginUpload's refusal — which reaches the handler's default arm — and
// the commit's refusal after the body streamed both answer as themselves
// instead of flattening into a 500. Unwraps to ErrImageLimitReached, so the
// arms that already match the sentinel keep matching. Needed is 0 when the
// refusal was made before any byte was read.
type ImageQuotaExceededError struct {
	Needed    int64
	Remaining int64
}

func (e *ImageQuotaExceededError) Error() string {
	if e.Needed > 0 {
		return fmt.Sprintf("this file needs %d bytes and the stored image quota has %d left — delete an image to free space", e.Needed, e.Remaining)
	}
	return "the stored image quota is full — delete an image to free space"
}

func (e *ImageQuotaExceededError) Unwrap() error     { return app_errors.ErrImageLimitReached }
func (e *ImageQuotaExceededError) HTTPStatus() int   { return http.StatusConflict }
func (e *ImageQuotaExceededError) ErrorCode() string { return "image_quota" }

var _ results.OperatorFacingError = (*ImageQuotaExceededError)(nil)

// uploadReservation is one admitted upload: its slot and the staging bytes it
// may come to hold. Keyed by image id, which the upload claim makes unique
// among in-flight uploads.
type uploadReservation struct {
	accountID uuid.UUID
	imageID   uuid.UUID
	bytes     int64
	// done is set by UploadFile when its final write cleared the claim, so
	// the lease's Release does not re-release a claim the record no longer
	// holds — a job may have taken it in between.
	done bool
}

// uploadAdmission bounds concurrent multipart staging.
//
// The count and byte quotas only see bytes that have been WRITTEN, and a
// multipart body is parsed in full — spilled to TMPDIR on the storage volume —
// before the service sees the first byte, so N concurrent uploads into one
// account all passed the same "remaining" check and held N times the quota in
// staging. This tracker is in-memory on purpose: a crash frees every slot
// with the process, which is correct, since the spills die with it too.
type uploadAdmission struct {
	mu            sync.Mutex
	maxGlobal     int
	maxPerAccount int
	// maxStagingBytes bounds the sum of every in-flight reservation; 0 = off
	// (tests only — the config layer never hands out 0).
	maxStagingBytes int64
	inflight        map[uuid.UUID]*uploadReservation // by image id
	perAccount      map[uuid.UUID]int
	// captures holds the byte reservations of capture streams in flight.
	// They take NO upload slot and NO staging bytes — a
	// capture streams straight to storage — but the bytes it may come to
	// store count against the account and instance ceilings exactly as an
	// admitted multipart upload's do, or two overlapping captures both read
	// the same remaining quota and both fit.
	captures map[uuid.UUID]*uploadReservation
}

func newUploadAdmission(maxGlobal, maxPerAccount int, maxStagingBytes int64) *uploadAdmission {
	return &uploadAdmission{
		maxGlobal:       maxGlobal,
		maxPerAccount:   maxPerAccount,
		maxStagingBytes: maxStagingBytes,
		inflight:        make(map[uuid.UUID]*uploadReservation),
		perAccount:      make(map[uuid.UUID]int),
		captures:        make(map[uuid.UUID]*uploadReservation),
	}
}

// reserveCapture records the bytes a capture stream may come to store, so
// every later quota read — another capture's, a multipart upload's — sees
// them as used until the stream settles. bytes is the stream's cap, which is
// what makes the reservation honest: the stream cannot store more than it.
func (a *uploadAdmission) reserveCapture(accountID, imageID uuid.UUID, bytes int64) *uploadReservation {
	if bytes < 0 {
		bytes = 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r := &uploadReservation{accountID: accountID, imageID: imageID, bytes: bytes}
	a.captures[imageID] = r
	return r
}

// releaseCapture gives a capture reservation back. Safe to call more than
// once; a reservation that was replaced by a newer one for the same record is
// left alone.
func (a *uploadAdmission) releaseCapture(r *uploadReservation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cur, ok := a.captures[r.imageID]; ok && cur == r {
		delete(a.captures, r.imageID)
	}
}

// acquire takes a slot and reserves bytes, or refuses with a typed 429. The
// per-account bound is asked FIRST so a tenant at its own ceiling hears about
// itself rather than about a server-wide pool it did not fill.
func (a *uploadAdmission) acquire(accountID, imageID uuid.UUID, bytes int64) (*uploadReservation, error) {
	if bytes < 0 {
		bytes = 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.maxPerAccount > 0 && a.perAccount[accountID] >= a.maxPerAccount {
		return nil, &UploadCapacityError{Limit: a.maxPerAccount, PerAccount: true}
	}
	if a.maxGlobal > 0 && len(a.inflight) >= a.maxGlobal {
		return nil, &UploadCapacityError{Limit: a.maxGlobal}
	}
	// Bytes, not just slots: the slot counts say how MANY bodies may be in
	// flight, this says how much TMPDIR they may be asked for together. Asked
	// after the counts so a tenant at its own ceiling still hears about
	// itself first. A body larger than the whole budget is refused even on
	// an idle server — the operator sized the budget for the disk.
	if a.maxStagingBytes > 0 {
		var inflight int64
		for _, r := range a.inflight {
			inflight += r.bytes
		}
		if inflight+bytes > a.maxStagingBytes {
			return nil, &UploadCapacityError{StagingBytes: true}
		}
	}
	if _, held := a.inflight[imageID]; held {
		// Two requests for the same record inside the window between one's
		// slot and its claim. The claim would refuse the second anyway; refuse
		// here so it never holds a slot it is about to give back.
		return nil, app_errors.ErrImageCompressionInProgress
	}
	r := &uploadReservation{accountID: accountID, imageID: imageID, bytes: bytes}
	a.inflight[imageID] = r
	a.perAccount[accountID]++
	return r, nil
}

// release gives the slot and the reservation back. Safe to call once per
// reservation; the lease's sync.Once guarantees that.
func (a *uploadAdmission) release(r *uploadReservation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cur, ok := a.inflight[r.imageID]; !ok || cur != r {
		return
	}
	delete(a.inflight, r.imageID)
	if a.perAccount[r.accountID] <= 1 {
		delete(a.perAccount, r.accountID)
	} else {
		a.perAccount[r.accountID]--
	}
}

// lookup returns the live reservation for a record, or nil. UploadFile uses
// it to tell a leased upload (claim already taken by BeginUpload) from a
// direct call (which claims for itself).
func (a *uploadAdmission) lookup(accountID, imageID uuid.UUID) *uploadReservation {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := a.inflight[imageID]
	if r == nil || r.accountID != accountID {
		return nil
	}
	return r
}

// markDone records that the upload's final write has cleared the claim.
func (a *uploadAdmission) markDone(r *uploadReservation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r.done = true
}

// reservedBytes sums the staging bytes other in-flight uploads may come to
// hold, for the account and for the whole server. exclude leaves one record's
// own reservation out, so an upload's own quota check does not count itself.
func (a *uploadAdmission) reservedBytes(accountID uuid.UUID, exclude *uuid.UUID) (account, global int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range []map[uuid.UUID]*uploadReservation{a.inflight, a.captures} {
		for id, r := range m {
			if exclude != nil && id == *exclude {
				continue
			}
			global += r.bytes
			if r.accountID == accountID {
				account += r.bytes
			}
		}
	}
	return account, global
}

// uploadLease is the services.UploadLease BeginUpload hands out.
type uploadLease struct {
	svc  *Service
	res  *uploadReservation
	once sync.Once
}

func (l *uploadLease) Release() {
	l.once.Do(func() {
		a := l.svc.admission
		a.mu.Lock()
		done := l.res.done
		a.mu.Unlock()
		a.release(l.res)
		if done {
			return
		}
		// Detached context, as in UploadFile: the commonest end of a multi-GB
		// upload is the client vanishing, which cancels the request context
		// exactly when the release matters most.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), releaseClaimTimeout)
		defer cancel()
		if err := l.svc.imageRepo.ReleaseUpload(releaseCtx, l.res.accountID, l.res.imageID); err != nil && l.svc.logger != nil {
			l.svc.logger.Warn("failed to release the upload claim", "image_id", l.res.imageID, "error", err)
		}
	})
}

var _ services.UploadLease = (*uploadLease)(nil)

// BeginUpload admits an upload before its body is read: slot, byte
// reservation, byte quota, account-scoped lookup, claim — in that order, the
// cheap in-memory bounds first, and nothing left held on any refusal.
func (s *Service) BeginUpload(ctx context.Context, accountID, imageID uuid.UUID, declaredBytes int64) (services.UploadLease, error) {
	if s.storageClient == nil {
		return nil, fmt.Errorf("storage client is not configured")
	}
	res, err := s.admitUpload(ctx, accountID, imageID, declaredBytes)
	if err != nil {
		return nil, err
	}
	// The account-scoped read is what makes a foreign or missing record a 404
	// before a byte of body has been read, rather than after the whole
	// multipart form has been staged.
	if _, err := s.imageRepo.GetByID(ctx, accountID, imageID); err != nil {
		s.admission.release(res)
		return nil, err
	}
	claimed, err := s.imageRepo.ClaimUpload(ctx, accountID, imageID)
	if err != nil {
		s.admission.release(res)
		return nil, fmt.Errorf("failed to claim the image for upload: %w", err)
	}
	if !claimed {
		s.admission.release(res)
		return nil, app_errors.ErrImageCompressionInProgress
	}
	return &uploadLease{svc: s, res: res}, nil
}

// admitUpload takes the slot and the byte reservation and reads what the
// byte quota has left as ONE step under quotaMu, the boundary the capture
// admission and the upload commit take too. The
// reservation goes into the tracker first, so a writer reading after this
// step sees it; the read then excludes this record — its own reservation
// and the file it is about to replace. A full quota is refused here, before
// a byte of body is read, and the slot is returned. The reservation is the
// declared body, which may be larger than what is left: whether the body
// itself fits is the handler's Content-Length check (with its framing slack)
// and, finally, commitUploadedBytes — an over-declared reservation only makes
// other writers read the account as fuller for the moment it is held.
func (s *Service) admitUpload(ctx context.Context, accountID, imageID uuid.UUID, declaredBytes int64) (*uploadReservation, error) {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	res, err := s.admission.acquire(accountID, imageID, declaredBytes)
	if err != nil {
		return nil, err
	}
	remaining, err := s.RemainingUploadBytes(ctx, accountID, &imageID)
	if err != nil {
		s.admission.release(res)
		return nil, err
	}
	s.pauseAt(quotaSiteUploadReserve)
	if remaining != services.NoByteCeiling && remaining <= 0 {
		s.admission.release(res)
		return nil, &ImageQuotaExceededError{}
	}
	return res, nil
}
