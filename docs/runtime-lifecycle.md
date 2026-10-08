# Host-owned environment lifecycle

Use `scenarioruntime.Lifecycle` when a runtime prepares external resources before
executing its existing prepared runtime. The host supplies setup, cleanup, a
positive cleanup deadline and public problem descriptions. Spex does not select
resources, credentials or cleanup targets.

Validate and plan first. Persist any required ownership evidence before calling
`Lifecycle.Execute`. The helper then:

1. Refuses to start setup after cancellation.
2. Attempts cleanup after setup starts, even if setup fails partway through.
3. Stops before test execution if setup fails or cancellation arrives.
4. Runs cleanup with an independent deadline so cancellation does not prevent it.
5. Keeps a failed or cancelled test outcome when cleanup also fails. A cleanup
   failure changes a passed outcome to an infrastructure error.

Callbacks must honor their contexts; the helper cannot forcibly stop arbitrary
in-process code. Setup and cleanup errors stay private. The host supplies safe
diagnostics through `SetupProblem` and `CleanupProblem` instead of exposing
command output or credentials. Test errors retain their original identity.

Successful outer cleanup does not erase a cleanup failure already reported by
the prepared runtime. Scheduling and resource coordinators still require their
own release verification; deleting temporary resources does not prove that
application state or in-flight work is safe.

For execution split across processes or CI steps, use the
[runtime host SDK](runtime-host-sdk.md). It persists admission/completion
checkpoints and resumes the same capacity ticket between steps.
