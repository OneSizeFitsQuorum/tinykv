// Copyright 2017 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package schedulers

import (
	"github.com/pingcap-incubator/tinykv/scheduler/server/core"
	"github.com/pingcap-incubator/tinykv/scheduler/server/schedule"
	"github.com/pingcap-incubator/tinykv/scheduler/server/schedule/operator"
	"github.com/pingcap-incubator/tinykv/scheduler/server/schedule/opt"
	"sort"
)

func init() {
	schedule.RegisterSliceDecoderBuilder("balance-region", func(args []string) schedule.ConfigDecoder {
		return func(v interface{}) error {
			return nil
		}
	})
	schedule.RegisterScheduler("balance-region", func(opController *schedule.OperatorController, storage *core.Storage, decoder schedule.ConfigDecoder) (schedule.Scheduler, error) {
		return newBalanceRegionScheduler(opController), nil
	})
}

const (
	// balanceRegionRetryLimit is the limit to retry schedule for selected store.
	balanceRegionRetryLimit = 10
	balanceRegionName       = "balance-region-scheduler"
)

type balanceRegionScheduler struct {
	*baseScheduler
	name         string
	opController *schedule.OperatorController
}

// newBalanceRegionScheduler creates a scheduler that tends to keep regions on
// each store balanced.
func newBalanceRegionScheduler(opController *schedule.OperatorController, opts ...BalanceRegionCreateOption) schedule.Scheduler {
	base := newBaseScheduler(opController)
	s := &balanceRegionScheduler{
		baseScheduler: base,
		opController:  opController,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// BalanceRegionCreateOption is used to create a scheduler with an option.
type BalanceRegionCreateOption func(s *balanceRegionScheduler)

func (s *balanceRegionScheduler) GetName() string {
	if s.name != "" {
		return s.name
	}
	return balanceRegionName
}

func (s *balanceRegionScheduler) GetType() string {
	return "balance-region"
}

func (s *balanceRegionScheduler) IsScheduleAllowed(cluster opt.Cluster) bool {
	return s.opController.OperatorCount(operator.OpRegion) < cluster.GetRegionScheduleLimit()
}

func (s *balanceRegionScheduler) Schedule(cluster opt.Cluster) *operator.Operator {
	stores := getHealthRegions(cluster)
	if len(stores) <= 1 {
		return nil
	}

	sort.Slice(stores, func(i, j int) bool {
		return stores[i].GetRegionSize() > stores[j].GetRegionSize()
	})

	var region *core.RegionInfo
	for sourceIndex := 0; sourceIndex < len(stores); sourceIndex++ {
		for i := 0; i < balanceRegionRetryLimit; i++ {
			cluster.GetPendingRegionsWithLock(stores[sourceIndex].GetID(), func(container core.RegionsContainer) {
				region = core.RandRegion(container)
			})
			if region == nil {
				cluster.GetFollowersWithLock(stores[sourceIndex].GetID(), func(container core.RegionsContainer) {
					region = core.RandRegion(container)
				})
			}
			if region == nil {
				cluster.GetLeadersWithLock(stores[sourceIndex].GetID(), func(container core.RegionsContainer) {
					region = core.RandRegion(container)
				})
			}
			if region != nil && len(region.GetMeta().GetPeers()) == cluster.GetMaxReplicas() {
				storesIds := region.GetStoreIds()
				for targetIndex := len(stores) - 1; targetIndex > sourceIndex; targetIndex-- {
					if _, ok := storesIds[stores[targetIndex].GetID()]; !ok && stores[sourceIndex].GetRegionSize()-stores[targetIndex].GetRegionSize() > 2*region.GetApproximateSize() {
						newPeer, err := cluster.AllocPeer(stores[targetIndex].GetID())
						if err != nil {
							return nil
						}
						op, err := operator.CreateMovePeerOperator("balance-region", cluster, region, operator.OpBalance, stores[sourceIndex].GetID(), stores[targetIndex].GetID(), newPeer.GetId())
						if err != nil {
							return nil
						}
						return op
					}
				}
			}
		}
	}
	return nil
}
