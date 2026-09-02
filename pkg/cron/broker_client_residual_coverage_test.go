//nolint:govet // Independent broker-response assertions intentionally reuse err.
package cron

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestCronBrokerClientResponseValidationMatrix(t *testing.T) {
	home := t.TempDir()
	var response any = cronBrokerResponse{}
	var responseErr error
	handler := database.HandlerFunc(func(context.Context, database.Request) (any, error) {
		return response, responseErr
	})
	server, err := database.StartServer(t.Context(), database.ServerOptions{Home: home, Handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	service := &CronService{brokerClient: client, storeID: BrokerStoreID, wakeChan: make(chan struct{}, 1)}
	every := int64(time.Minute / time.Millisecond)
	validJob := CronJob{
		ID: "job", Name: "job", Enabled: true,
		Schedule: CronSchedule{Kind: "every", EveryMS: &every},
		Payload:  CronPayload{Kind: "agent_turn"},
	}

	responseErr = database.NewError(database.CodeUnavailable, "down")
	if _, err := service.AddJob("name", validJob.Schedule, "", "", ""); database.CodeOf(
		err,
	) != database.CodeUnavailable ||
		service.initErr == nil {
		t.Fatalf("AddJob transport error = %v, init=%v", err, service.initErr)
	}
	responseErr = nil
	response = cronBrokerResponse{}
	if _, err := service.AddJob("name", validJob.Schedule, "", "", ""); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("AddJob nil response error = %v", err)
	}
	invalidJob := validJob
	invalidJob.ID = ""
	response = cronBrokerResponse{Job: &invalidJob}
	if _, err := service.AddJob("name", validJob.Schedule, "", "", ""); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("AddJob invalid response error = %v", err)
	}
	response = cronBrokerResponse{Job: &validJob}
	added, err := service.AddJob("name", validJob.Schedule, "", "", "")
	if err != nil || added == nil || added.ID != validJob.ID {
		t.Fatalf("AddJob valid response = %#v, %v", added, err)
	}
	added.Name = "detached"
	if validJob.Name != "job" {
		t.Fatal("AddJob response aliased transport")
	}

	responseErr = database.NewError(database.CodeUnavailable, "down")
	if job, found := service.GetJob("job"); job != nil || found || service.initErr == nil {
		t.Fatalf("GetJob transport response = %#v/%v", job, found)
	}
	responseErr = nil
	response = cronBrokerResponse{Found: false, Job: &validJob}
	if job, found := service.GetJob("job"); job != nil || found ||
		database.CodeOf(service.initErr) != database.CodeIntegrity {
		t.Fatalf("GetJob contradictory response = %#v/%v/%v", job, found, service.initErr)
	}
	response = cronBrokerResponse{Found: true}
	if job, found := service.GetJob("job"); job != nil || found ||
		database.CodeOf(service.initErr) != database.CodeIntegrity {
		t.Fatalf("GetJob nil response = %#v/%v/%v", job, found, service.initErr)
	}
	response = cronBrokerResponse{Found: true, Job: &invalidJob}
	if job, found := service.GetJob("job"); job != nil || found ||
		database.CodeOf(service.initErr) != database.CodeIntegrity {
		t.Fatalf("GetJob invalid response = %#v/%v/%v", job, found, service.initErr)
	}
	response = cronBrokerResponse{Found: true, Job: &validJob}
	if job, found := service.GetJob("job"); !found || job == nil || job.ID != "job" {
		t.Fatalf("GetJob valid response = %#v/%v", job, found)
	}
	response = cronBrokerResponse{Found: false}
	if job, found := service.GetJob("missing"); found || job != nil || service.initErr != nil {
		t.Fatalf("GetJob missing response = %#v/%v/%v", job, found, service.initErr)
	}

	responseErr = database.NewError(database.CodeConflict, "conflict")
	if err := service.UpdateJob(&validJob); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("UpdateJob transport error = %v", err)
	}
	responseErr = nil
	response = cronBrokerResponse{}
	if err := service.UpdateJob(&validJob); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("UpdateJob nil response error = %v", err)
	}
	response = cronBrokerResponse{Job: &invalidJob}
	if err := service.UpdateJob(&validJob); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("UpdateJob invalid response error = %v", err)
	}
	response = cronBrokerResponse{Job: &validJob}
	if err := service.UpdateJob(&validJob); err != nil {
		t.Fatalf("UpdateJob valid response error = %v", err)
	}

	responseErr = database.NewError(database.CodeUnavailable, "down")
	if service.RemoveJob("job") || service.initErr == nil {
		t.Fatal("RemoveJob transport failure succeeded")
	}
	responseErr = nil
	response = cronBrokerResponse{Removed: true}
	if !service.RemoveJob("job") {
		t.Fatal("RemoveJob valid response failed")
	}

	responseErr = database.NewError(database.CodeUnavailable, "down")
	if service.EnableJob("job", true) != nil || service.initErr == nil {
		t.Fatal("EnableJob transport failure succeeded")
	}
	responseErr = nil
	response = cronBrokerResponse{Found: false}
	if service.EnableJob("job", true) != nil {
		t.Fatal("EnableJob missing response returned job")
	}
	response = cronBrokerResponse{Found: true}
	if service.EnableJob("job", true) != nil || database.CodeOf(service.initErr) != database.CodeIntegrity {
		t.Fatalf("EnableJob nil response error = %v", service.initErr)
	}
	response = cronBrokerResponse{Found: true, Job: &invalidJob}
	if service.EnableJob("job", true) != nil || database.CodeOf(service.initErr) != database.CodeIntegrity {
		t.Fatalf("EnableJob invalid response error = %v", service.initErr)
	}
	response = cronBrokerResponse{Found: true, Job: &validJob}
	if job := service.EnableJob("job", true); job == nil || job.ID != "job" {
		t.Fatalf("EnableJob valid response = %#v", job)
	}

	responseErr = database.NewError(database.CodeUnavailable, "down")
	status := service.Status()
	if status["jobs"] != 0 || service.initErr == nil {
		t.Fatalf("Status transport failure = %#v/%v", status, service.initErr)
	}
	responseErr = nil
	for _, invalid := range []cronBrokerResponse{{}, {Status: &cronStatusResponse{Jobs: -1}}} {
		response = invalid
		status = service.Status()
		if status["jobs"] != 0 || database.CodeOf(service.initErr) != database.CodeIntegrity {
			t.Errorf("Status invalid response = %#v/%v", status, service.initErr)
		}
	}
	next := time.Now().UnixMilli()
	response = cronBrokerResponse{Status: &cronStatusResponse{Enabled: true, Jobs: 2, NextWakeAtMS: &next}}
	status = service.Status()
	if status["jobs"] != 2 || status["enabled"] != true || service.initErr != nil {
		t.Fatalf("Status valid response = %#v/%v", status, service.initErr)
	}

	response = cronBrokerResponse{Jobs: []CronJob{validJob}, NextCursor: 0, Done: false}
	if jobs := service.ListJobs(true); jobs != nil || database.CodeOf(service.initErr) != database.CodeIntegrity {
		t.Fatalf("ListJobs invalid cursor = %#v/%v", jobs, service.initErr)
	}
	response = cronBrokerResponse{Jobs: []CronJob{invalidJob}, NextCursor: 1, Done: true}
	if jobs := service.ListJobs(true); jobs != nil || database.CodeOf(service.initErr) != database.CodeIntegrity {
		t.Fatalf("ListJobs invalid job = %#v/%v", jobs, service.initErr)
	}
	response = cronBrokerResponse{Jobs: []CronJob{validJob}, NextCursor: 1, Done: true}
	if jobs := service.ListJobs(true); len(jobs) != 1 || jobs[0].ID != "job" || service.initErr != nil {
		t.Fatalf("ListJobs valid response = %#v/%v", jobs, service.initErr)
	}
	validJob.Enabled = false
	response = cronBrokerResponse{Jobs: []CronJob{validJob}, NextCursor: 1, Done: true}
	if jobs := service.ListJobs(false); len(jobs) != 0 {
		t.Fatalf("ListJobs disabled filter = %#v", jobs)
	}

	if service.StoreID() != BrokerStoreID {
		t.Fatalf("StoreID = %q", service.StoreID())
	}
	if (*CronService)(nil).StoreID() != "" {
		t.Fatal("nil service StoreID was nonempty")
	}
	if _, err := NewOfflineService(filepath.Join(home, "cron"), nil); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unfenced offline service error = %v", err)
	}
}
