package bcadmin_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
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

func ExampleClient_ForTenant() {
	var client *bcadmin.Client // from bcadmin.NewClient with a multi-tenant app
	if client == nil {
		return
	}
	// The same app registration manages the environments of another tenant.
	customer, err := client.ForTenant("00000000-0000-0000-0000-000000000002")
	if err != nil {
		log.Fatal(err)
	}
	envs, err := customer.Environments().List(context.Background(), bcadmin.FamilyBusinessCentral)
	if err != nil {
		log.Fatal(err)
	}
	for _, env := range envs {
		fmt.Println(env.Name, env.Type, env.ApplicationVersion)
	}
}

func ExampleAppsService_Install() {
	var env *bcadmin.Environment // from client.Environments().Get
	if env == nil {
		return
	}
	ctx := context.Background()
	poller, err := env.Apps().Install(ctx, "00000000-0000-0000-0000-000000000020", bcadmin.InstallAppOptions{
		AcceptISVEULA: true, // accepts the app provider's license terms
	})
	if re, ok := errors.AsType[*bcadmin.ResponseError](err); ok && re.Data != nil {
		// Missing dependencies; install them first or set
		// InstallOrUpdateNeededDependencies.
		for _, r := range re.Data.Requirements {
			fmt.Println("requires", r.Name, r.Version)
		}
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	op, err := poller.Wait(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(op.Status)
}

func ExampleAppsService_InstallPTE() {
	var env *bcadmin.Environment // from client.Environments().Get
	if env == nil {
		return
	}
	f, err := os.Open("Contoso_MyApp_1.0.0.0.app")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := installPTE(context.Background(), env, f); err != nil {
		fmt.Println(err)
	}
}

// installPTE installs the per-tenant extension in f in env.
func installPTE(ctx context.Context, env *bcadmin.Environment, f *os.File) error {
	// The file is streamed, not loaded into memory.
	poller, err := env.Apps().InstallPTE(ctx, f, bcadmin.InstallPTEOptions{
		FileName:           "Contoso_MyApp_1.0.0.0.app",
		DeploymentSchedule: bcadmin.DeploymentUpdateWindow,
		AcceptISVEULA:      true,
	})
	if err != nil {
		return err
	}
	// Return as soon as the installation is scheduled for the update window.
	op, err := poller.Wait(ctx, &bcadmin.WaitOptions{TreatScheduledAsDone: true})
	if err != nil {
		return err
	}
	fmt.Println(op.Status)
	return nil
}

func ExampleUpdatesService_Select() {
	var env *bcadmin.Environment // from client.Environments().Get
	if env == nil {
		return
	}
	ctx := context.Background()
	updates, err := env.Updates().List(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, u := range updates {
		if !u.Available {
			continue
		}
		// Update tonight, inside the environment's update window.
		start := time.Now().Add(12 * time.Hour)
		if err := env.Updates().Select(ctx, u.TargetVersion, &bcadmin.ScheduleUpdateOptions{SelectedDateTime: start}); err != nil {
			log.Fatal(err)
		}
		fmt.Println("selected", u.TargetVersion)
		return
	}
}

func ExampleSettingsService_SetUpdateWindow() {
	var env *bcadmin.Environment // from client.Environments().Get
	if env == nil {
		return
	}
	w, err := env.Settings().SetUpdateWindow(context.Background(), bcadmin.SetUpdateWindowOptions{
		Start: "22:00", End: "06:00", TimeZoneID: "W. Europe Standard Time",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%+v\n", w)
}

func ExampleExportsService_Start() {
	var env *bcadmin.Environment // from client.Environments().Get
	if env == nil {
		return
	}
	ctx := context.Background()
	m, err := env.ExportMetrics(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if m.ExportsRemainingThisMonth == 0 {
		fmt.Println("no exports left this month")
		return
	}
	// The SAS URI is a secret; ExportOptions masks it when printed.
	opts := bcadmin.ExportOptions{
		StorageAccountSASURI: os.Getenv("EXPORT_SAS_URI"),
		Container:            "exports",
		Blob:                 env.Name + ".bacpac",
	}
	if _, err := env.ExportDatabase(ctx, opts); err != nil {
		log.Fatal(err)
	}
	fmt.Println("export started:", opts)
}
