# nfConfigApi models 

nfConfigApi models are used to implement nfConfig Server/client services.

The provided models are generated using `webconsole-api.yaml` in this folder by ensuring the struct definitions (including required fields) are kept in sync with the OpenAPI specification.

## How to Regenerate Models
 
To regenerate nfConfig models, after updating `webconsole-api.yaml`, please run the following command:

```
npx openapi-generator-cli version

sudo openapi-generator-cli generate \
  -i ./webconsole-api.yaml \
  -g go-gin-server \
  -o ./webconsole-server \
  --additional-properties=validateRequired=true
```




