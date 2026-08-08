kubectl-sidercars
=================

A `kubectl` extension to view sidecar information for running pods in a
kubernete cluster.

Example usage:
``` sh
# List all the pods in all namespace that have sidecars with `istio` in their
# image name and are currently running (i.e. not `Completed`,
# `CrashLoopBackOff`, etc.)

kubectl sidecars -A --running-only '*istio*'

# List all sidecars in the `myserver` namespace

kubectl sidercars -n myserver

```
