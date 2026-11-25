package bcadmin_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/axiansinfoma/go-bcadmin"
)

func ExampleNewClient() {
	// Any azcore.TokenCredential works, for example a client secret, a
	// workload identity or the Azure CLI.
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		log.Fatal(err)
	}
	client, err := bcadmin.NewClient(cred, &bcadmin.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Telemetry: policy.TelemetryOptions{ApplicationID: "my-tool/1.0"},
		},
		PollFrequency: 30 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(client.Endpoint())
	// Output: https://api.businesscentral.dynamics.com/admin/v2.29
}

func ExampleTenantService_Quotas() {
	var client *bcadmin.Client // from bcadmin.NewClient
	if client == nil {
		return
	}
	quotas, err := client.Tenant().Quotas(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(quotas.EnvironmentsCount.Production, quotas.EnvironmentsCount.Sandbox)
}

func ExampleTenantService_ResumeOperation() {
	var client *bcadmin.Client // from bcadmin.NewClient
	if client == nil {
		return
	}
	// Pick up an operation started earlier, for example by another process.
	poller, err := client.Tenant().ResumeOperation("00000000-0000-0000-0000-000000000010")
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	op, err := poller.Wait(ctx, nil)
	if oe, ok := errors.AsType[*bcadmin.OperationError](err); ok {
		fmt.Println("operation failed:", oe.Operation.ErrorMessage)
		return
	}
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(op.Status)
}

func ExampleResponseError() {
	var client *bcadmin.Client // from bcadmin.NewClient
	if client == nil {
		return
	}
	_, err := client.Tenant().Quotas(context.Background())
	if re, ok := errors.AsType[*bcadmin.ResponseError](err); ok {
		// The API's own error code and message, plus the request ID for support.
		fmt.Println(re.StatusCode, re.Code, re.Message, re.RequestID)
	}
}

func ExampleEnvironmentsService_Create() {
	var client *bcadmin.Client // from bcadmin.NewClient
	if client == nil {
		return
	}
	ctx := context.Background()
	poller, err := client.Environments().Create(ctx, bcadmin.FamilyBusinessCentral, "uat", bcadmin.CreateEnvironmentOptions{
		Type:        bcadmin.EnvironmentTypeSandbox,
		CountryCode: "DE",
	})
	if err != nil {
		fmt.Println(bcadmin.ErrorCode(err), err) // for example "resourceExists"
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	env, err := poller.Wait(waitCtx, &bcadmin.WaitOptions{UntilEnvironmentActive: true})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(env.Name, env.Status, env.WebClientLoginURL)
}
