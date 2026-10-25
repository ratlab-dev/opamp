Feature: Reproducible Rendering and Zero-Diff Detection
  In order to prevent unnecessary OTEL collector restarts on target VMs
  As an OpAMP server administrator
  I need configuration rendering to be 100% deterministic and byte-for-byte stable

  Scenario: Rendered YAML and SHA-256 hash are reproducible across multiple renders
    Given a base configuration with receivers "otlp"
    And an environment override for "prod" with exporters "datadog"
    When the configuration is rendered 5 consecutive times
    Then every render must produce the identical SHA-256 hash

  Scenario: Server detects agent already has matching hash and sends zero diff
    Given an agent connected with reported config hash "matching"
    When the server evaluates effective config matching hash "matching"
    Then the server should mark the agent as in-sync
    And the server should not send a remote config update payload
