// Package costs estimates the cost per namespace from what each pod reserves
// (requests) and, if metrics-server is present, from what it consumes. It is
// an allocation with unit instance prices, not billing: it knows nothing about
// spot instances, discounts or different node types. Dedicated cost tools
// exist for that.
package costs

import (
	"sort"

	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/settings"
)

// HoursPerMonth is the usual convention (365 × 24 / 12).
const HoursPerMonth = 730.0

const gib = 1024 * 1024 * 1024

// Namespace is one row of the report.
type Namespace struct {
	Namespace          string `json:"namespace"`
	Pods               int    `json:"pods"`
	CPURequestMilli    int64  `json:"cpuRequestMilli"`
	MemoryRequestBytes int64  `json:"memoryRequestBytes"`
	CPUUsedMilli       int64  `json:"cpuUsedMilli"`
	MemoryUsedBytes    int64  `json:"memoryUsedBytes"`
	// Monthly cost of what is RESERVED (requests) and of what is USED (metrics).
	// Used only makes sense with available=true.
	ReservedMonthly float64 `json:"reservedMonthly"`
	UsedMonthly     float64 `json:"usedMonthly"`
	// Billable: max(reserved, used) per resource, the usual cost allocation
	// criterion: a pod that requests 2 cores and uses 0.1 costs 2;
	// one that requests 0 and uses 1 costs 1.
	BillableMonthly float64 `json:"billableMonthly"`
}

// Report is the response of GET /api/clusters/{id}/costs.
type Report struct {
	Available     bool           `json:"available"`
	Reason        string         `json:"reason,omitempty"`
	Configured    bool           `json:"configured"`
	Prices        settings.Costs `json:"prices"`
	HoursPerMonth float64        `json:"hoursPerMonth"`
	Namespaces    []Namespace    `json:"namespaces"`
	Totals        Namespace      `json:"totals"`
}

// Aggregate groups pods by namespace and applies the prices. Pure.
func Aggregate(list k8s.PodUsageList, prices settings.Costs) Report {
	byNS := map[string]*Namespace{}
	for _, p := range list.Pods {
		n, ok := byNS[p.Namespace]
		if !ok {
			n = &Namespace{Namespace: p.Namespace}
			byNS[p.Namespace] = n
		}
		n.Pods++
		n.CPURequestMilli += p.CPURequestMilli
		n.MemoryRequestBytes += p.MemoryRequestBytes
		n.CPUUsedMilli += p.CPUUsedMilli
		n.MemoryUsedBytes += p.MemoryUsedBytes
	}
	rep := Report{Available: list.Available, Reason: list.Reason, Configured: prices.Configured(), Prices: prices, HoursPerMonth: HoursPerMonth, Namespaces: []Namespace{}}
	for _, n := range byNS {
		price(n, prices, list.Available)
		rep.Namespaces = append(rep.Namespaces, *n)
		rep.Totals.Pods += n.Pods
		rep.Totals.CPURequestMilli += n.CPURequestMilli
		rep.Totals.MemoryRequestBytes += n.MemoryRequestBytes
		rep.Totals.CPUUsedMilli += n.CPUUsedMilli
		rep.Totals.MemoryUsedBytes += n.MemoryUsedBytes
	}
	price(&rep.Totals, prices, list.Available)
	sort.Slice(rep.Namespaces, func(i, j int) bool {
		if rep.Namespaces[i].BillableMonthly != rep.Namespaces[j].BillableMonthly {
			return rep.Namespaces[i].BillableMonthly > rep.Namespaces[j].BillableMonthly
		}
		return rep.Namespaces[i].Namespace < rep.Namespaces[j].Namespace
	})
	return rep
}

func monthly(cpuMilli, memBytes int64, prices settings.Costs) float64 {
	return (float64(cpuMilli)/1000*prices.CPUCoreHour + float64(memBytes)/gib*prices.MemoryGiBHour) * HoursPerMonth
}

func price(n *Namespace, prices settings.Costs, metrics bool) {
	n.ReservedMonthly = monthly(n.CPURequestMilli, n.MemoryRequestBytes, prices)
	if !metrics {
		n.BillableMonthly = n.ReservedMonthly
		return
	}
	n.UsedMonthly = monthly(n.CPUUsedMilli, n.MemoryUsedBytes, prices)
	n.BillableMonthly = monthly(max(n.CPURequestMilli, n.CPUUsedMilli), max(n.MemoryRequestBytes, n.MemoryUsedBytes), prices)
}
