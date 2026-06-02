package workspace

type Inputs struct {
	ScenarioPath           string
	BindingPath            string
	ScenarioName           string
	Namespace              string
	KubeContext            string
	RunID                  string
	StartKIND              bool
	RepoRoot               string
	IntegrationProfilePath string
	CatalogPaths           []string
	Integration            *IntegrationProfile
	Scenario               Scenario
	Binding                TargetBinding
}

type Scenario struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   Metadata     `yaml:"metadata"`
	Spec       ScenarioSpec `yaml:"spec"`
}

type Metadata struct {
	Name string   `yaml:"name"`
	Tags []string `yaml:"tags,omitempty"`
}

type ScenarioSpec struct {
	Description      string                     `yaml:"description"`
	Parameters       map[string]Parameter       `yaml:"parameters"`
	Defaults         Defaults                   `yaml:"defaults"`
	Correlation      Correlation                `yaml:"correlation"`
	PayloadTemplates map[string]PayloadTemplate `yaml:"payloadTemplates"`
	GraphQLQueries   map[string]GraphQLQuery    `yaml:"graphqlQueries"`
	Use              []FlowUse                  `yaml:"use"`
	StepInvocations  []StepInvocation           `yaml:"stepInvocations"`
	Operations       []Operation                `yaml:"operations"`
}

type Parameter struct {
	Type    string `yaml:"type"`
	Default string `yaml:"default"`
}

type Defaults struct {
	Timeout      string `yaml:"timeout"`
	PollInterval string `yaml:"pollInterval"`
}

type Correlation struct {
	ScenarioRunID string `yaml:"scenarioRunId"`
	Strategy      string `yaml:"strategy"`
}

type PayloadTemplate struct {
	ContentType string `yaml:"contentType"`
	Body        string `yaml:"body"`
}

type GraphQLQuery struct {
	File string `yaml:"file"`
}

type Operation struct {
	ID       string                 `yaml:"id"`
	Type     string                 `yaml:"type"`
	After    string                 `yaml:"after"`
	MQTT     *MQTTPublish           `yaml:"mqtt"`
	Redpanda *RedpandaContains      `yaml:"redpanda"`
	GraphQL  *GraphQLExpectation    `yaml:"graphql"`
	MongoDB  *MongoDBExpectation    `yaml:"mongodb"`
	Postgres *PostgreSQLExpectation `yaml:"postgresql"`
	RabbitMQ *RabbitMQOperation     `yaml:"rabbitmq"`
}

type MQTTPublish struct {
	Topic              string `yaml:"topic"`
	PayloadTemplateRef string `yaml:"payloadTemplateRef"`
	CorrelationID      string `yaml:"correlationId"`
}

type RedpandaContains struct {
	TopicRef      string    `yaml:"topicRef"`
	CorrelationID string    `yaml:"correlationId"`
	Timeout       string    `yaml:"timeout"`
	Match         []Matcher `yaml:"match"`
}

type GraphQLExpectation struct {
	QueryRef  string            `yaml:"queryRef"`
	Variables map[string]string `yaml:"variables"`
	Timeout   string            `yaml:"timeout"`
	Match     []Matcher         `yaml:"match"`
}

type MongoDBExpectation struct {
	Collection    string    `yaml:"collection"`
	Filter        string    `yaml:"filter"`
	CorrelationID string    `yaml:"correlationId"`
	Timeout       string    `yaml:"timeout"`
	Match         []Matcher `yaml:"match"`
}

type PostgreSQLExpectation struct {
	Query         string    `yaml:"query"`
	Args          []string  `yaml:"args"`
	CorrelationID string    `yaml:"correlationId"`
	Timeout       string    `yaml:"timeout"`
	Match         []Matcher `yaml:"match"`
}

type RabbitMQOperation struct {
	Exchange           string    `yaml:"exchange"`
	RoutingKey         string    `yaml:"routingKey"`
	Queue              string    `yaml:"queue"`
	PayloadTemplateRef string    `yaml:"payloadTemplateRef"`
	CorrelationID      string    `yaml:"correlationId"`
	Timeout            string    `yaml:"timeout"`
	Match              []Matcher `yaml:"match"`
}

type Matcher struct {
	Path         string `yaml:"path"`
	EqualsString string `yaml:"equalsString"`
	EqualsNumber string `yaml:"equalsNumber"`
	EqualsBool   *bool  `yaml:"equalsBool"`
	EqualsNull   *bool  `yaml:"equalsNull"`
}

type TargetBinding struct {
	APIVersion string      `yaml:"apiVersion"`
	Kind       string      `yaml:"kind"`
	Metadata   Metadata    `yaml:"metadata"`
	Spec       BindingSpec `yaml:"spec"`
}

type BindingSpec struct {
	KubeContext        string            `yaml:"kubeContext"`
	Namespace          string            `yaml:"namespace"`
	ScenarioParameters map[string]string `yaml:"scenarioParameters"`
	RBAC               RBAC              `yaml:"rbac"`
	Probe              Probe             `yaml:"probe"`
	Secrets            map[string]Secret `yaml:"secrets"`
	MQTT               MQTTBinding       `yaml:"mqtt"`
	Redpanda           RedpandaBinding   `yaml:"redpanda"`
	GraphQL            GraphQLBinding    `yaml:"graphql"`
	MongoDB            MongoDBBinding    `yaml:"mongodb"`
	PostgreSQL         PostgreSQLBinding `yaml:"postgresql"`
	RabbitMQ           RabbitMQBinding   `yaml:"rabbitmq"`
}

type RBAC struct {
	Create bool `yaml:"create"`
}

type Probe struct {
	Image              string `yaml:"image"`
	ImagePullPolicy    string `yaml:"imagePullPolicy"`
	ServiceAccountName string `yaml:"serviceAccountName"`
}

type Secret struct {
	Type          string            `yaml:"type"`
	Name          string            `yaml:"name"`
	Keys          map[string]string `yaml:"keys"`
	EnvFile       string            `yaml:"envFile"`
	Env           map[string]string `yaml:"env"`
	SSMParameters map[string]string `yaml:"ssmParameters"`
}

type MQTTBinding struct {
	BrokerURL      string `yaml:"brokerURL"`
	ClientIDPrefix string `yaml:"clientIdPrefix"`
	CredentialsRef string `yaml:"credentialsRef"`
}

type RedpandaBinding struct {
	Brokers string                   `yaml:"brokers"`
	Topics  map[string]RedpandaTopic `yaml:"topics"`
}

type RedpandaTopic struct {
	Name                string `yaml:"name"`
	AllowOffsetSnapshot bool   `yaml:"allowOffsetSnapshot"`
	AllowCompacted      bool   `yaml:"allowCompacted"`
}

type GraphQLBinding struct {
	Endpoint       string      `yaml:"endpoint"`
	CredentialsRef string      `yaml:"credentialsRef"`
	Auth           GraphQLAuth `yaml:"auth"`
}

type MongoDBBinding struct {
	Deployment     string `yaml:"deployment"`
	URI            string `yaml:"uri"`
	Database       string `yaml:"database"`
	CredentialsRef string `yaml:"credentialsRef"`
}

type PostgreSQLBinding struct {
	URI            string `yaml:"uri"`
	CredentialsRef string `yaml:"credentialsRef"`
}

type RabbitMQBinding struct {
	URI            string `yaml:"uri"`
	CredentialsRef string `yaml:"credentialsRef"`
}

type GraphQLAuth struct {
	Type            string   `yaml:"type"`
	TokenURL        string   `yaml:"tokenURL"`
	ClientID        string   `yaml:"clientID"`
	ClientSecretRef string   `yaml:"clientSecretRef"`
	Scopes          []string `yaml:"scopes"`
}

type IntegrationProfile struct {
	APIVersion string                 `yaml:"apiVersion"`
	Kind       string                 `yaml:"kind"`
	Spec       IntegrationProfileSpec `yaml:"spec"`
}

type IntegrationProfileSpec struct {
	AllowFakes bool             `yaml:"allowFakes"`
	Extends    []string         `yaml:"extends"`
	KIND       KINDIntegration  `yaml:"kind"`
	Setup      SetupIntegration `yaml:"setup"`
	HelmApps   []HelmApp        `yaml:"helmApps"`
}

type KINDIntegration struct {
	Start       bool           `yaml:"start"`
	ClusterName string         `yaml:"clusterName"`
	Config      string         `yaml:"config"`
	NodeCache   *bool          `yaml:"nodeCache"`
	Containers  []string       `yaml:"containers"`
	Commands    []KUTTLCommand `yaml:"commands"`
}

type SetupIntegration struct {
	Commands []KUTTLCommand `yaml:"commands"`
}

type KUTTLCommand struct {
	Command string `yaml:"command"`
	Timeout int    `yaml:"timeout"`
}

type HelmApp struct {
	Name      string            `yaml:"name"`
	Chart     string            `yaml:"chart"`
	Repo      string            `yaml:"repo"`
	Namespace string            `yaml:"namespace"`
	Values    []string          `yaml:"values"`
	Set       map[string]string `yaml:"set"`
	Wait      *bool             `yaml:"wait"`
	Timeout   string            `yaml:"timeout"`
}

type ScenarioSuite struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   Metadata          `yaml:"metadata"`
	Spec       ScenarioSuiteSpec `yaml:"spec"`
}

type ScenarioSuiteSpec struct {
	BindingRef            string        `yaml:"bindingRef"`
	IntegrationProfileRef string        `yaml:"integrationProfileRef"`
	CatalogRefs           []string      `yaml:"catalogRefs"`
	Scenarios             []ScenarioRef `yaml:"scenarios"`
	WorkspaceDir          string        `yaml:"workspaceDir"`
	FailFast              bool          `yaml:"failFast"`
	Reports               SuiteReports  `yaml:"reports"`
}

type ScenarioRef struct {
	Path                  string            `yaml:"path"`
	BindingRef            string            `yaml:"bindingRef"`
	IntegrationProfileRef string            `yaml:"integrationProfileRef"`
	Parameters            map[string]string `yaml:"parameters"`
	Tags                  []string          `yaml:"tags"`
}

type SuiteReports struct {
	OutputDir string   `yaml:"outputDir"`
	Format    []string `yaml:"format"`
}

type ResolvedScenarioRef struct {
	Path                   string
	BindingPath            string
	IntegrationProfilePath string
	Parameters             map[string]string
	Tags                   []string
}

type ResolvedScenarioSuite struct {
	Path                   string
	Suite                  ScenarioSuite
	BindingPath            string
	IntegrationProfilePath string
	CatalogPaths           []string
	ScenarioPaths          []string
	ScenarioRefs           []ResolvedScenarioRef
}

type FlowUse struct {
	Flow string            `yaml:"flow"`
	ID   string            `yaml:"id"`
	With map[string]string `yaml:"with"`
}

type StepInvocation struct {
	Kind string `yaml:"kind"`
	Text string `yaml:"text"`
}

type CatalogExpansion struct {
	Parameters       map[string]Parameter       `yaml:"parameters"`
	PayloadTemplates map[string]PayloadTemplate `yaml:"payloadTemplates"`
	GraphQLQueries   map[string]GraphQLQuery    `yaml:"graphqlQueries"`
	Operations       []Operation                `yaml:"operations"`
}

type CatalogBundle struct {
	Flows     map[string]FlowDefinition
	Steps     []StepDefinition
	Inventory CatalogInventory
}

type CatalogFlow struct {
	Name   string
	Source string
	Flow   FlowDefinition
}

type CatalogStep struct {
	Source string
	Step   StepDefinition
}

type CatalogInventory struct {
	Flows []CatalogFlow
	Steps []CatalogStep
}

type FlowCatalog struct {
	APIVersion string          `yaml:"apiVersion"`
	Kind       string          `yaml:"kind"`
	Metadata   Metadata        `yaml:"metadata"`
	Spec       FlowCatalogSpec `yaml:"spec"`
}

type FlowCatalogSpec struct {
	Flows map[string]FlowDefinition `yaml:"flows"`
}

type FlowDefinition struct {
	Parameters map[string]string `yaml:"parameters"`
	ExpandsTo  CatalogExpansion  `yaml:"expandsTo"`
}

type StepCatalog struct {
	APIVersion string          `yaml:"apiVersion"`
	Kind       string          `yaml:"kind"`
	Metadata   Metadata        `yaml:"metadata"`
	Spec       StepCatalogSpec `yaml:"spec"`
}

type StepCatalogSpec struct {
	Steps []StepDefinition `yaml:"steps"`
}

type StepDefinition struct {
	Kind       string           `yaml:"kind"`
	Expression string           `yaml:"expression"`
	Output     CatalogExpansion `yaml:"output"`
}
