Kubernetes Admission Webhook example  
https://kubernetes.io/docs/reference/access-authn-authz/admission-controllers/

### install
1. 创建对应的命名空间
    ```shell script
    kubectl create ns webhook-example
    ```
2. 安装证书
    ```shell script
    ./deployment/webhook-create-signed-cert.sh
    kubectl -n webhook-example get secrets admission-webhook-example-certs
    ```
3. 部署admission-webhook-example
    ```shell script
    kubectl apply -f deployment/deployment.yaml
    kubectl apply -f deployment/service.yaml
    ```
4. 创建webhook资源
    ```shell script
    cat ./deployment/mutatingwebhook.yaml | ./deployment/webhook-patch-ca-bundle.sh | kubectl apply -f -
    cat ./deployment/validatingwebhook.yaml | ./deployment/webhook-patch-ca-bundle.sh | kubectl apply -f -
    ```
5. 验证
    * 部署测试deployment
    ```shell script 
    kubectl label ns demo  admission-webhook-example=enabled
    kubectl apply -f  deployment/demo-deploy-svc.yaml
    ```
    * 查看日志
    ```shell script
    $ kubectl -n webhook-example logs  -f admission-webhook-example-deployment-6876f7ff89-5tmbs
    I1124 07:31:33.684616       1 main.go:55] Server started
    I1124 07:38:25.544726       1 webhook.go:347] ######### /mutate ##########
    ......
    I1124 07:38:25.552429       1 webhook.go:347] ######### /validate ##########
    ......
    ```

参考
> https://github.com/morvencao/kube-mutating-webhook-tutorial
> https://github.com/denverdino/lxcfs-admission-webhook
> https://github.com/cnych/admission-webhook-example
