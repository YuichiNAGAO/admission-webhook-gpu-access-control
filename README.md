# admission-webhook-gpu-access-control


### Problem

If you don't request GPUs when using the device plugin with NVIDIA images all the GPUs on the machine will be exposed inside your container. This admission webhook disable GPU access of pods requesting zero GPUs.
Issue: https://github.com/NVIDIA/k8s-device-plugin/issues/61


### How to use

Go to a server where you can run `kubectl` and run the follwoing command.
`image resistory` is something like `docker.io/<user name>`.

```
git clone 
bash entrypoint.sh <image resistory>
```
