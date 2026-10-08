# Owned Kind lifecycle

`pkg/ownedkind` provides native Go cluster and image operations. The host supplies
the execution name, private state directory, kubeconfig, cluster configuration
and image repository. Spex does not choose service images or project defaults.

Create a `Cluster` with `New(Config)`, then call `Create`, `LoadImage` and
`Cleanup` with a context. The host must first write a configuration receipt in
the private state directory. Its `scope` must match the cluster name and its
`suite` must identify the prepared input. The library accepts a single
control-plane Kind configuration without extra mounts or port mappings.

## Ownership checks

Creation refuses pre-existing nodes with the requested Kind label. Create-only
ledgers record the attempt and exact Docker node IDs, including nodes left by a
failed or cancelled creation. Cancellation stops the child tool process; a
separate bounded context captures remaining nodes for cleanup.

Cleanup checks the entire current node population against the ledger before
deleting any node. It removes only verified full node IDs. It never invokes
`kind delete cluster`, prunes Docker, or adopts resources by cluster name alone.
Missing ownership evidence requires manual review.

Images use private tags derived from the execution name and alias. Completion
receipts record immutable image IDs. Cleanup removes a private tag only while
it still refers to the recorded ID. Compatibility aliases live inside verified
nodes, not on the shared daemon. Shared source images remain untouched.

`Fingerprint` hashes the typed image plan and explicit host-owned source inputs.
It rejects links and paths outside the checkout, and excludes timestamps and
absolute checkout locations. The host chooses those inputs and binds the hash
to its preparation receipt; changed inputs must not silently reuse stale images.

Incomplete image preparation does not prevent deletion of independently
verified nodes. Cleanup still returns an error and retains uncertain evidence;
the host must not release scheduled capacity as though cleanup completed.

## Host responsibilities

The host owns admission, scheduling, trusted image/build selection, credentials,
reporting and recovery policy. It must preserve the private ledgers and handle
cleanup errors separately from the primary test result. The library does not
provide AWS isolation or coordinate shared application data.

Tests inject a typed `Runner` to exercise shared-daemon behavior. The optional
`TestLiveDockerOwnership` uses `SPEX_OWNED_KIND_TEST_IMAGE` to check exact-container
and private-tag cleanup against Docker. It does not qualify real Kind creation
or an acceptance stack.

`TestLiveKindLifecycle` accepts `SPEX_LIVE_KIND_NODE_IMAGE` for a cached node
image. It creates two disposable clusters, loads a private scratch image and
checks that exact-node cleanup preserves the peer. It pulls no node image and
does not provision an application stack.
