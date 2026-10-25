I want to build a custom opamp server.
  The server picks the configuration from base and overlays.
  We have overlays based on hostname, environment, role, cluster, subdomain etc.
  Define order of override.
  There are env specific and role specific overrides. Define them and the vbehaviour

  Further, the goal is to write a UI, that lets one see the effective configuration, and guides the
  user how to make the change in the git repository for the stuff.
  For local development mode, we use local directory for gitOps/without gitops.
  When delivering configuration, have option to use git directory as well. gitea, gitlab and github
  option.

  ALso make a feature that lets user override the configuration from the UI temporarily on some basis
  for say duration like 24h ,6h. 12h and so on. However, persist in redis with a respective path, and
  a TTL. This is the final overlay on top of what gitops have. If there are two similar paths, redis
  one gets the precedence in getting the configuration.

  The OTEL collector running in the target VM should not restart when there is no effective change in
  the config. So the config needs to be rendered in reproducible way and send no diff when there is
  actually none.

  The UI should be intuitive to an operations person.

  Also, Guidance on onboarding new kinds of eollectors like node exporter, clickhouse collector, and
  so on should be published.. Also we need a human understandable testsuite too. Write the stack in Go


  1. Go ahead with Gitea implentation first leg. It should be reusable for other providers.
  Also, it should be deployable through K8S configmaps too. Multiple Kubernetes Configmaps could be
  mounted to the app's file system while running in production mode.
  2. The UI based temp override should be done based on the existing effective confg. Existing
  effective overlay config(not the entire one) gets filled. But the user should understand what's
  going to happen etc.
  3. agreed.

  development mode should allow read and write to the config file system
  production mode should allow reads from the FileSystem/Git Repository, and allow only TTL based
  updates to redis. Also we need a webhook based configuration to publish audit information when
  something changes. Define a standard for the webhook thing too.
