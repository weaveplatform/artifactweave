package model_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/weaveplatform/artifactweave/internal/releases/core/model"
)

func TestUnitDistribution(t *testing.T) {
	if !model.BundleOpen.Editable() || model.BundleSigned.Editable() {
		t.Error("Editable")
	}
	targets := []model.DistributionTarget{
		{PeerInstanceID: "a", SiteName: "edge-london", CityName: "London", CountryCode: "GB"},
		{PeerInstanceID: "b", SiteName: "edge-paris", CityName: "Paris", CountryCode: "FR"},
		{PeerInstanceID: "c", SiteName: "core", CityName: "London", CountryCode: "GB"},
	}
	cases := []struct {
		rules []model.DistributionRule
		want  []string
	}{
		{[]model.DistributionRule{{SiteName: "edge-*"}}, []string{"a", "b"}},
		{[]model.DistributionRule{{CityName: "london"}}, []string{"a", "c"}},
		{[]model.DistributionRule{{CountryCodes: []string{"fr"}}}, []string{"b"}},
		{
			[]model.DistributionRule{{SiteName: "edge-*", CountryCodes: []string{"GB"}}},
			[]string{"a"},
		},
		{[]model.DistributionRule{{SiteName: "["}}, nil},
		{[]model.DistributionRule{{}}, []string{"a", "b", "c"}},
		{nil, nil},
	}
	for _, c := range cases {
		var got []string
		for _, tg := range model.SelectTargets(c.rules, targets) {
			got = append(got, tg.PeerInstanceID)
		}
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%+v (-want +got):\n%s", c.rules, diff)
		}
	}
	job := func(s model.JobStatus) model.DistributionJob { return model.DistributionJob{Status: s} }
	statuses := []struct {
		jobs []model.DistributionJob
		want model.JobStatus
	}{
		{nil, model.JobNotDistributed},
		{
			[]model.DistributionJob{job(model.JobCompleted), job(model.JobCompleted)},
			model.JobCompleted,
		},
		{
			[]model.DistributionJob{job(model.JobCompleted), job(model.JobInProgress)},
			model.JobInProgress,
		},
		{
			[]model.DistributionJob{job(model.JobAborted), job(model.JobInProgress)},
			model.JobAborted,
		},
		{
			[]model.DistributionJob{job(model.JobInProgress), job(model.JobAborted)},
			model.JobAborted,
		},
		{[]model.DistributionJob{job(model.JobAborted), job(model.JobFailed)}, model.JobFailed},
		{
			[]model.DistributionJob{job(model.JobCompleted), job(model.JobInQueue)},
			model.JobInProgress,
		},
	}
	for _, s := range statuses {
		if got := model.TrackerStatus(s.jobs); got != s.want {
			t.Errorf("%+v = %s, want %s", s.jobs, got, s.want)
		}
	}
}
