package model_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitDistribution(t *testing.T) {
	if !types.BundleOpen.Editable() || types.BundleSigned.Editable() {
		t.Error("Editable")
	}
	targets := []types.DistributionTarget{
		{PeerInstanceID: "a", SiteName: "edge-london", CityName: "London", CountryCode: "GB"},
		{PeerInstanceID: "b", SiteName: "edge-paris", CityName: "Paris", CountryCode: "FR"},
		{PeerInstanceID: "c", SiteName: "core", CityName: "London", CountryCode: "GB"},
	}
	cases := []struct {
		rules []types.DistributionRule
		want  []string
	}{
		{[]types.DistributionRule{{SiteName: "edge-*"}}, []string{"a", "b"}},
		{[]types.DistributionRule{{CityName: "london"}}, []string{"a", "c"}},
		{[]types.DistributionRule{{CountryCodes: []string{"fr"}}}, []string{"b"}},
		{
			[]types.DistributionRule{{SiteName: "edge-*", CountryCodes: []string{"GB"}}},
			[]string{"a"},
		},
		{[]types.DistributionRule{{SiteName: "["}}, nil},
		{[]types.DistributionRule{{}}, []string{"a", "b", "c"}},
		{nil, nil},
	}
	for _, c := range cases {
		var got []string
		for _, tg := range types.SelectTargets(c.rules, targets) {
			got = append(got, tg.PeerInstanceID)
		}
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%+v (-want +got):\n%s", c.rules, diff)
		}
	}
	job := func(s types.JobStatus) types.DistributionJob { return types.DistributionJob{Status: s} }
	statuses := []struct {
		jobs []types.DistributionJob
		want types.JobStatus
	}{
		{nil, types.JobNotDistributed},
		{
			[]types.DistributionJob{job(types.JobCompleted), job(types.JobCompleted)},
			types.JobCompleted,
		},
		{
			[]types.DistributionJob{job(types.JobCompleted), job(types.JobInProgress)},
			types.JobInProgress,
		},
		{
			[]types.DistributionJob{job(types.JobAborted), job(types.JobInProgress)},
			types.JobAborted,
		},
		{
			[]types.DistributionJob{job(types.JobInProgress), job(types.JobAborted)},
			types.JobAborted,
		},
		{[]types.DistributionJob{job(types.JobAborted), job(types.JobFailed)}, types.JobFailed},
		{
			[]types.DistributionJob{job(types.JobCompleted), job(types.JobInQueue)},
			types.JobInProgress,
		},
	}
	for _, s := range statuses {
		if got := types.TrackerStatus(s.jobs); got != s.want {
			t.Errorf("%+v = %s, want %s", s.jobs, got, s.want)
		}
	}
}
