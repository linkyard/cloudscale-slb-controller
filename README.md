# cloudscale-slb-controller

A kubernetes controller that watches for services of type `LoadBalancer` and creates a floating IP
on cloudscale.ch for those services. The floating IP will be attached to the server where one of
the services pods in state `Running` is located.

It is safe to run multiple replicas of this controller.

## supported service annotations

The following annotations on services of type `LoadBalancer` are supported:

- `linkyard.ch/slb-controller-id`: this service must be processed by a controller with the given id
- `linkyard.ch/existing-floating-ip`: use the already existing floating IP on cloudscale.ch for this
  service; note that no additional checks (e.g. port collisions) are performed

## configuration

The following configuration flags are supported:

- `-cloudscale-token`, `CLOUDSCALE_TOKEN`, no default, required: cloudscale.ch API token with 
  write access
- `-ip-limit`, `IP_LIMIT`, default `5`: maximum number of floating ips that are allowed to exist on
  the cloudscale.ch account; this is used as an additional guard in case this controller is out of
  control and would create an infinite amount of floating ips
- `-controller-id`, `CONTROLLER_ID`, no default: if set, the controller will only process services
  with a `linkyard.ch/slb-controller-id` annotation set to this value
- `-leader-election-configmap`, `LEADER_ELECTION_CONFIGMAP`, no default: name of the `ConfigMap` 
  to use for leader election
- `-leader-election-namespace` `LEADER_ELECTION_NAMESPACE`, no default: name of the namespace where 
  the `ConfigMap` for leader election is located at
- `-leader-election-node`, `LEADER_ELECTION_NODE`, no default: name of the pod
- `-leader-election-ttl`, `LEADER_ELECTION_TTL`, default `10s`: TTL for leader election, e.g. `10s`;
  valid time units are `ns`, `us`, `ms`, `s`, `m` and `h`.
- `-log-level`, `LOG_LEVEL`, default `Debug`: log level to set; possible values are:
  `Panic`, `Fatal`, `Error`, `Warn`, `Info`, `Debug`, `Trace`
- `-kubeconfig`, `KUBECONFIG`, no default, not required: path to a kubernetes kubconfig file; this
  is not required when running inside a kubernetes cluster
- `-fake-cloudscale-client`, `FAKE_CLOUDSCALE_CLIENT`: set to `true` to use a fake cloudscale.ch 
  API client
- `-chaos-chance`, `CHAOS_CHANCE`, default `0`: chance of a call to one of the fake components failing; 
  range `[0.0,1,0)`
