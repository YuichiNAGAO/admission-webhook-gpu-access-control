# admission-webhook-gpu-access-control


### Problem

If you don't request GPUs when using the device plugin with NVIDIA images all the GPUs on the machine will be exposed inside your container. This admission webhook disable GPU access of pods requesting zero GPUs.

Issue: https://github.com/NVIDIA/k8s-device-plugin/issues/61


### How to use

Please run the following commend where `kubectl` is enabled. 
`image resistory` is something like `docker.io/<user name>`.

```
git clone https://github.com/YuichiNAGAO/admission-webhook-gpu-access-control.git
bash entrypoint.sh <image resistory>
```
