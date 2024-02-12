package vaultsync

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2/hclsimple"
	vault "github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/api/auth/approle"
	"github.com/hashicorp/vault/api/auth/ldap"
	"github.com/hashicorp/vault/api/auth/userpass"
)

// config struct defines the structure of the configuration file.
type config struct {
	Vault vaultConfig `hcl:"config,block"`
}

// vaultConfig struct defines the configuration for connecting to Vault.
type vaultConfig struct {
	Server             string `hcl:"server"`
	AuthMethod         string `hcl:"authmethod"`
	Username           string `hcl:"username"`
	Password           string `hcl:"password"`
	RenewSecretsPeriod int64  `hcl:"renew_secrets_period"`
}

// SecretReceiver interface defines the method for updating secrets.
type SecretReceiver interface {
	UpdateSecret(id string, filedName string, value interface{})
}

// SecretSync struct manages secret receivers.
type SecretSync struct {
	receivers map[string][]SecretReceiver
}

// AgentOptFunc type defines a function that modifies AgentOpts.
type AgentOptFunc func(*AgentOpts)

// AgentOpts struct defines options for configuring the Agent.
type AgentOpts struct {
	log         *slog.Logger
	logLevelVar *slog.LevelVar
	configFile  string
}

// Agent struct represents the Agent with its options and configuration.
type Agent struct {
	AgentOpts
	config     *config
	client     *vault.Client
	secret     *vault.Secret
	secretSync *SecretSync
}

// defaultAgentOpts function creates default options for the Agent.
func defaultAgentOpts() AgentOpts {
	agentOpts := AgentOpts{}

	// Logging
	agentOpts.logLevelVar = &slog.LevelVar{}
	loggerOpts := &slog.HandlerOptions{
		Level: agentOpts.logLevelVar,
	}
	agentOpts.logLevelVar.Set(slog.LevelDebug) // Set debug as default.
	agentOpts.log = slog.New(slog.NewTextHandler(os.Stdout, loggerOpts))

	// default vault config file
	agentOpts.configFile = "vault-config.hcl"

	return agentOpts
}

// WithConfigFile function sets the configuration file path.
func WithConfigFile(file string) AgentOptFunc {
	return func(opts *AgentOpts) {
		opts.configFile = file
	}
}

// WithLogger function sets the logger.
func WithLogger(log *slog.Logger) AgentOptFunc {
	return func(opts *AgentOpts) {
		opts.log = log
	}
}

// WithLogLevel function sets the log level.
func WithLogLevel(logLevel string) AgentOptFunc {
	return func(opts *AgentOpts) {
		switch strings.ToUpper(logLevel) {
		case slog.LevelDebug.Level().String():
			opts.logLevelVar.Set(slog.LevelDebug)

		case slog.LevelWarn.Level().String():
			opts.logLevelVar.Set(slog.LevelWarn)

		case slog.LevelError.Level().String():
			opts.logLevelVar.Set(slog.LevelError)

		default: // default to Info
			opts.logLevelVar.Set(slog.LevelInfo)
		}

	}
}

// newSecretSync function creates a new SecretSync.
func newSecretSync() *SecretSync {
	return &SecretSync{
		receivers: make(map[string][]SecretReceiver),
	}
}

// RegisterUpdateSecret method registers a secret receiver.
func (a *Agent) RegisterUpdateSecret(id string, receiver SecretReceiver) {
	a.secretSync.receivers[id] = append(a.secretSync.receivers[id], receiver)
}

// setSecret method sets a secret value for a receiver.
func (a *Agent) setSecret(id string, fieldName string, value interface{}) {
	for _, receiver := range a.secretSync.receivers[id] {
		receiver.UpdateSecret(id, fieldName, value)
	}
}

// New function creates a new Vault sync agent with provided options.
func New(opts ...AgentOptFunc) (*Agent, error) {
	agent := &Agent{}
	agent.secretSync = newSecretSync()
	var err error

	agentOpts := defaultAgentOpts()
	for _, fn := range opts {
		fn(&agentOpts)
	}
	agent.AgentOpts = agentOpts

	agent.log.Info("NewAgent", slog.String("config file", agent.configFile))

	// Load configuration from file
	err = agent.loadConfig(agent.configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration file %v:%v", agent.configFile, err)
	}

	agent.log.Debug("NewAgent", slog.Any("config", agent.config))

	// Create vault agent and auhtenticate
	err = agent.createVaultAgent()
	if err != nil {
		return nil, fmt.Errorf("authentication failed:%v", err)
	}

	return agent, nil
}

// Run method starts the Agent. Once Run returns secrets should be available by the caller.
func (a *Agent) Run(ctx context.Context, wg *sync.WaitGroup) error {

	wg.Add(2)
	go a.renewAuthToken(ctx, wg)
	go a.renewSecrets(ctx, wg)

	// Update all registered secret paths before returning to the caller.
	// This should make sure that variables in all registred structs has a vaule
	// after Run() returns.
	a.renewSecretPaths()

	return nil
}

// loadConfig method loads vault agent configuration from the given filename.
func (a *Agent) loadConfig(filename string) error {
	_, err := os.Stat(filename)
	if !os.IsNotExist(err) {
		a.config = &config{}
		err := hclsimple.DecodeFile(filename, nil, a.config)
		if err != nil {
			return err
		}
	}

	return nil
}

// createVaultAgent creates as vault agent and handles authentication.
// Possible values for authMethod is: "approle", "ldap", "userpass".
// If the authentication method is "approle", then username contains the role_id and the password the secret_id.
func (a *Agent) createVaultAgent() error {
	var err error

	// Create vault client
	a.client, err = vault.NewClient(&vault.Config{
		Address: a.config.Vault.Server,
	})
	if err != nil {
		return err
	}

	// Authenticate against vault and get an authentication token.
	switch a.config.Vault.AuthMethod {
	case "approle":
		authMethod, err := approle.NewAppRoleAuth(a.config.Vault.Username, &approle.SecretID{FromString: a.config.Vault.Password})
		if err != nil {
			return err
		}
		a.secret, err = a.client.Auth().Login(context.TODO(), authMethod)
		if err != nil {
			return err
		}
		a.log.Info("createVaultAgent", slog.String("AuthMethod", "approle"))

	case "ldap":
		authMethod, err := ldap.NewLDAPAuth(a.config.Vault.Username, &ldap.Password{FromString: a.config.Vault.Password})
		if err != nil {
			return err
		}
		a.secret, err = a.client.Auth().Login(context.TODO(), authMethod)
		if err != nil {
			return err
		}
		a.log.Info("createVaultAgent", slog.String("AuthMethod", "ldap"))

	case "userpass":
		authMethod, err := userpass.NewUserpassAuth(a.config.Vault.Username, &userpass.Password{FromString: a.config.Vault.Password})
		if err != nil {
			return err
		}
		a.secret, err = a.client.Auth().Login(context.TODO(), authMethod)
		if err != nil {
			return err
		}
		a.log.Info("createVaultAgent", slog.String("AuthMethod", "userpass"))

	default:
		a.log.Error("createVaultAgent", slog.String("error", "undefined vault authentication method"))
		return fmt.Errorf("undefined vault authentication method")
	}

	token, err := a.secret.TokenID()
	if err != nil {
		return err
	}
	a.client.SetToken(token)

	return nil
}

// renewAuthToken method renews the authentication token.
func (a *Agent) renewAuthToken(ctx context.Context, wg *sync.WaitGroup) error {
	defer wg.Done()

	authTokenWatcher, err := a.client.NewLifetimeWatcher(&vault.LifetimeWatcherInput{
		Secret: a.secret,
	})
	if err != nil {
		return fmt.Errorf("unable to initialize auth token lifetime watcher: %w", err)
	}

	go authTokenWatcher.Start()
	defer authTokenWatcher.Stop()

	// monitor events from watcher
	for {
		select {
		case <-ctx.Done():
			a.log.Info("renewAuthToken", slog.String("status", "cancel"))
			return nil

		// DoneCh will return if renewal fails, or if the remaining lease
		// duration is under a built-in threshold and either renewing is not
		// extending it or renewing is disabled.  In both cases, the caller
		// should attempt a re-read of the secret. Clients should check the
		// return value of the channel to see if renewal was successful.
		case err := <-authTokenWatcher.DoneCh():
			// Leases created by a token get revoked when the token is revoked.
			a.log.Info("renewAuthToken", slog.String("status", "renewal of auth token failed"), slog.Any("error", err))
			return err

		// RenewCh is a channel that receives a message when a successful
		// renewal takes place and includes metadata about the renewal.
		case info := <-authTokenWatcher.RenewCh():
			a.log.Info("renewAuthToken", slog.String("status", "renewed"), slog.Any("remaining duration", info.Secret.Auth.LeaseDuration))
		}
	}
}

// renewSecretPaths reads secrets from vault and then executes the registerd update secrets functions for each vault secret.
func (a *Agent) renewSecretPaths() {
	for path := range a.secretSync.receivers {
		secret, _ := a.client.Logical().Read(path)
		for key, value := range secret.Data["data"].(map[string]interface{}) {
			a.setSecret(path, key, value)
		}
		a.log.Info("renewSecrets", slog.String("secret-path", path), slog.Any("seconds until next renew secret", a.config.Vault.RenewSecretsPeriod))
	}
}

// renewSecrets method renew secrets periodically.
func (a *Agent) renewSecrets(ctx context.Context, wg *sync.WaitGroup) error {
	defer wg.Done()

	sleepDuration := time.Duration(a.config.Vault.RenewSecretsPeriod) * time.Second
	timer := time.NewTimer(sleepDuration)

	for {
		select {
		case <-ctx.Done():
			a.log.Info("reneswSecrets", slog.String("status", "cancel"))
			return nil

		case <-timer.C:
			a.renewSecretPaths()
			// Reset the timer for the next iteration
			timer.Reset(sleepDuration)
		}
	}
}
