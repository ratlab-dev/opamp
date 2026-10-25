Feature: Configuration Merging and Precedence
  In order to provide flexible and environment-specific OTEL configurations
  As an OpAMP server administrator
  I need to be able to merge base configurations with various overlays according to strict precedence rules

  Scenario: Environment override for exporters
    Given a base configuration with receivers "otlp"
    And an environment override for "prod" with exporters "datadog"
    When the effective configuration is rendered for environment "prod"
    Then the configuration should have receivers "otlp"
    And the configuration should have exporters "datadog"

  Scenario: Role override with higher precedence than base
    Given a base configuration with "otlp" processor
    And a role override for "database" with "memory_limiter" processor
    When the effective configuration is rendered for role "database"
    Then the configuration should include processor "otlp"
    And the configuration should include processor "memory_limiter"

  Scenario: Cluster and Subdomain overlays
    Given a base configuration with receivers "otlp"
    And a cluster override for "us-east-1" with region "us-east-1"
    And a subdomain override for "payments" with tag "pci-dss"
    When the effective configuration is rendered for cluster "us-east-1" and subdomain "payments"
    Then the configuration should have region "us-east-1"
    And the configuration should have tag "pci-dss"

  Scenario: Environment-specific role override
    Given a base configuration with interval "10s"
    And a role override for "database" with interval "5s"
    And an environment-role override for "prod" and "database" with interval "2s"
    When the effective configuration is rendered for role "database" in environment "prod"
    Then the configuration interval should be "2s"

  Scenario: Redis temporary override takes highest precedence
    Given a base configuration with interval "10s"
    And an environment override for "prod" with interval "30s"
    And a temporary redis override for hostname "node-1" with interval "1s"
    When the effective configuration is rendered for hostname "node-1" in environment "prod"
    Then the configuration interval should be "1s"
