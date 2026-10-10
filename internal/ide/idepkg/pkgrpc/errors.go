// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package pkgrpc

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/auth"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc/pkgrpcpb"
)

// ErrUnsupported is returned when the host runs a Rune that does not
// serve package management.
var ErrUnsupported = errors.New("the host does not support package management")

// UpdateHostMessage tells the user what to do about ErrUnsupported
// from host.
func UpdateHostMessage(host string) string {
	return fmt.Sprintf("Update Rune on %s to install packages there.", host)
}

var errorKinds = []struct {
	kind pkgrpcpb.PackageError_Kind
	err  error
	code codes.Code
}{
	{pkgrpcpb.PackageError_NOT_INSTALLED, idepkg.ErrNotInstalled, codes.NotFound},
	{pkgrpcpb.PackageError_PACKAGE_NOT_FOUND, idepkg.ErrPackageNotFound, codes.NotFound},
	{pkgrpcpb.PackageError_VERSION_NOT_FOUND, idepkg.ErrVersionNotFound, codes.NotFound},
	{pkgrpcpb.PackageError_ALREADY_INSTALLED, idepkg.ErrAlreadyInstalled, codes.AlreadyExists},
	{pkgrpcpb.PackageError_VERSION_IN_USE, idepkg.ErrVersionInUse, codes.FailedPrecondition},
	{pkgrpcpb.PackageError_SERVER_UNAVAILABLE, idepkg.ErrServerUnavailable, codes.Unavailable},
	{pkgrpcpb.PackageError_ARTIFACT_MISSING, idepkg.ErrArtifactMissing, codes.NotFound},
	{pkgrpcpb.PackageError_FORBIDDEN, idepkg.ErrForbidden, codes.PermissionDenied},
	{pkgrpcpb.PackageError_NO_RELEASES, idepkg.ErrNoReleases, codes.NotFound},
	{pkgrpcpb.PackageError_NOT_AUTHENTICATED, auth.ErrNotAuthenticated, codes.Unauthenticated},
}

// toStatus encodes err for the wire, tagging it with the idepkg sentinel
// it wraps so the client can restore it. Errors that already are a gRPC
// status pass through.
func toStatus(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	for _, k := range errorKinds {
		if !errors.Is(err, k.err) {
			continue
		}
		st, detailErr := status.New(k.code, err.Error()).
			WithDetails(&pkgrpcpb.PackageError{Kind: k.kind})
		if detailErr != nil {
			return status.Error(k.code, err.Error())
		}
		return st.Err()
	}
	return status.Error(codes.Unknown, err.Error())
}

// fromStatus restores the error a server encoded with toStatus.
func fromStatus(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.Unimplemented:
		return ErrUnsupported
	case codes.Canceled:
		return &remoteError{msg: st.Message(), err: context.Canceled}
	case codes.DeadlineExceeded:
		return &remoteError{msg: st.Message(), err: context.DeadlineExceeded}
	}
	for _, d := range st.Details() {
		pe, ok := d.(*pkgrpcpb.PackageError)
		if !ok {
			continue
		}
		for _, k := range errorKinds {
			if k.kind == pe.GetKind() {
				return &remoteError{msg: st.Message(), err: k.err}
			}
		}
	}
	return err
}

// remoteError carries the host's error message while matching the
// sentinel it wrapped there.
type remoteError struct {
	msg string
	err error
}

func (e *remoteError) Error() string { return e.msg }

func (e *remoteError) Unwrap() error { return e.err }
