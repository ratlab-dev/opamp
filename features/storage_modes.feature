Feature: Operating Modes and Storage Providers
  In order to enforce GitOps safety in production while allowing fast iteration in dev
  As an OpAMP administrator
  I need development mode to allow filesystem writes and production mode to reject them

  Scenario: Development mode permits writing configuration files
    Given a local store running in "development" mode
    When the operator writes a "roles" config named "redis-cache"
    Then the file should be successfully written to disk

  Scenario: Production mode prohibits writing configuration files
    Given a local store running in "production" mode
    When the operator writes a "roles" config named "redis-cache"
    Then the write operation must be rejected with a read-only error

  Scenario: Kubernetes ConfigMap store aggregates multiple mount paths
    Given a Kubernetes ConfigMap store with mounts for "base" and "roles"
    When the effective configuration is read for role "database"
    Then both base and role configurations should be loaded from the mounts
