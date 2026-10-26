package costs

import (
	"math"
	"testing"

	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/settings"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestAggregate(t *testing.T) {
	prices := settings.Costs{CPUCoreHour: 0.04, MemoryGiBHour: 0.005, Currency: "EUR"}
	list := k8s.PodUsageList{Available: true, Pods: []k8s.PodUsage{
		{Namespace: "web", Name: "a", CPURequestMilli: 2000, MemoryRequestBytes: 1 * gib, CPUUsedMilli: 100, MemoryUsedBytes: 2 * gib},
		{Namespace: "web", Name: "b", CPURequestMilli: 0, MemoryRequestBytes: 0, CPUUsedMilli: 1000, MemoryUsedBytes: 0},
		{Namespace: "db", Name: "c", CPURequestMilli: 500, MemoryRequestBytes: 4 * gib},
	}}
	rep := Aggregate(list, prices)
	if !rep.Configured || len(rep.Namespaces) != 2 {
		t.Fatalf("report: %+v", rep)
	}
	web := rep.Namespaces[0]
	if web.Namespace != "web" || web.Pods != 2 {
		t.Fatalf("order by cost: %+v", rep.Namespaces)
	}
	// Reserved: 2 cores × 0.04 + 1 GiB × 0.005 = 0.085/h × 730.
	if !near(web.ReservedMonthly, 0.085*730) {
		t.Fatalf("reserved web = %v", web.ReservedMonthly)
	}
	// Used: 1.1 cores × 0.04 + 2 GiB × 0.005 = 0.054/h.
	if !near(web.UsedMonthly, 0.054*730) {
		t.Fatalf("used web = %v", web.UsedMonthly)
	}
	// Billable: max(2000,1100) cores and max(1,2) GiB = 0.08 + 0.01.
	if !near(web.BillableMonthly, 0.09*730) {
		t.Fatalf("billable web = %v", web.BillableMonthly)
	}
	if !near(rep.Totals.ReservedMonthly, web.ReservedMonthly+rep.Namespaces[1].ReservedMonthly) {
		t.Fatalf("totals: %+v", rep.Totals)
	}
}

func TestAggregateWithoutMetricsUsesReserved(t *testing.T) {
	prices := settings.Costs{CPUCoreHour: 1}
	list := k8s.PodUsageList{Available: false, Reason: "no metrics", Pods: []k8s.PodUsage{{Namespace: "a", CPURequestMilli: 1000}}}
	rep := Aggregate(list, prices)
	n := rep.Namespaces[0]
	if rep.Available || n.UsedMonthly != 0 || !near(n.BillableMonthly, 730) || !near(n.ReservedMonthly, 730) {
		t.Fatalf("without metrics: %+v", n)
	}
}

func TestAggregateUnconfigured(t *testing.T) {
	rep := Aggregate(k8s.PodUsageList{Pods: []k8s.PodUsage{{Namespace: "a", CPURequestMilli: 1000}}}, settings.Costs{})
	if rep.Configured || rep.Namespaces[0].BillableMonthly != 0 || rep.Namespaces[0].CPURequestMilli != 1000 {
		t.Fatalf("without prices it must list resources with cost 0: %+v", rep)
	}
}
