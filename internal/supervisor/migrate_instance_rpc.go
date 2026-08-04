// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"context"
	"fmt"
	"time"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/core/migration"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// Live instance migration (GOBLIN-DIV-031).
//
// Distinct from SchedulerRPC.MigrateJob, which REASSIGNS a job to
// another node - it stops work in one place and starts it in another,
// and the process does not survive. This moves a RUNNING process with
// its memory intact: the instance UUID is unchanged and only its
// locator moves. The two verbs share the RightJobMigrate right because
// both hand an instance to a different node; they are not otherwise
// interchangeable.

// migrationEpoch derives the checkpoint epoch for a new attempt.
//
// Wall-clock milliseconds rather than a counter: the epoch only has to
// be monotonic per instance so a retry cannot collide with an earlier
// image, and a counter would need durable per-instance state that the
// FSM does not otherwise carry.
func migrationEpoch(now time.Time) uint64 {
	return uint64(now.UnixMilli())
}

// MigrateInstance live-migrates a running instance to another node.
func (s *SchedulerRPC) MigrateInstance(req *goblinv1.MigrateInstanceRequest, resp *goblinv1.MigrateInstanceResponse) error {
	if s.consensus == nil {
		return fmt.Errorf("consensus not initialized on this node")
	}
	if s.scheduler == nil {
		return fmt.Errorf("scheduler not initialized on this node")
	}

	instanceID := req.GetInstanceId()
	toNode := req.GetToNode()

	uuid, err := ident.Parse(instanceID)
	if err != nil {
		return fmt.Errorf("instance id %q: %w", instanceID, err)
	}

	// Authorize against the INSTANCE, not the node or the job: a token
	// minted for one instance must not move another.
	payload, token, err := s.authorizeToken(capability.VerbJobMigrate, uuid)
	if err != nil {
		return err
	}

	inst, ok := s.consensus.GetInstance(instanceID)
	if !ok {
		return fmt.Errorf("instance %s is not known to the cluster", instanceID)
	}
	if inst.GetNodeId() == "" {
		return fmt.Errorf("instance %s has no current node", instanceID)
	}
	if inst.GetNodeId() == toNode {
		return fmt.Errorf("instance %s already runs on %s", instanceID, toNode)
	}

	// The destination must be able to instantiate the same agent type,
	// so the spec travels with the restore request.
	spec, err := s.scheduler.GetAgent(context.Background(), ident.String(inst.GetSpecUuid()))
	if err != nil {
		return fmt.Errorf("resolving spec for instance %s: %w", instanceID, err)
	}

	coord := s.migrationCoordinator()
	if coord == nil {
		return fmt.Errorf("migration is not configured on this node")
	}

	sourceNode := inst.GetNodeId()
	err = coord.Migrate(context.Background(), migration.Request{
		InstanceID:   instanceID,
		InstanceUUID: uuid,
		SourceNode:   sourceNode,
		TargetNode:   toNode,
		Epoch:        migrationEpoch(time.Now()),
		Rights:       payload.GetRights(),
		Token:        token,
		Spec:         spec,
	})
	// The token's job ends with the migration, whichever way it went.
	// Revoking on FAILURE matters more than on success: a rolled-back or
	// stranded migration is exactly when a token is loose with no
	// completion to bound it, and until this existed nothing ever called
	// Revoke in production (GOBLIN-DIV-015). Without it the only bound
	// on a leaked migrate token - which reads another process's entire
	// address space - was its 60-300s TTL.
	s.revokeToken(context.Background(), payload.GetTokenId())

	if err != nil {
		// The coordinator's typed outcomes matter to an operator:
		// ErrRolledBack means the instance is still serving on its
		// source, ErrStranded means it is running nowhere. Both are
		// returned verbatim so goblinctl can say which.
		return err
	}

	resp.InstanceId = instanceID
	resp.FromNode = sourceNode
	resp.ToNode = toNode
	return nil
}

// migrationCoordinator builds a coordinator from the collaborators the
// supervisor wired. Returns nil when migration is not configured, which
// MigrateInstance reports rather than half-running.
//
// Built per call rather than stored: a Coordinator is a plain struct
// over three interfaces with no state of its own, and constructing it
// here keeps the nil-check and the construction in one place.
func (s *SchedulerRPC) migrationCoordinator() *migration.Coordinator {
	if s.migrateNodes == nil || s.migrateRaft == nil {
		return nil
	}
	return migration.NewCoordinator(
		s.migrateRaft,
		s.migrateNodes,
		migration.NewRPCPuller(s.migrateNodes),
		s.migrateLogger,
	)
}
