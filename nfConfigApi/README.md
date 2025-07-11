<!--
# SPDX-FileCopyrightText: 2025 Canonical Ltd

SPDX-License-Identifier: Apache-2.0
-->

# nfConfigApi Models

nfConfigApi models are used to implement nfConfig Server/client services.

The provided models are generated using `webconsole-api.yaml` in this folder,
ensuring the struct definitions (including required fields) are kept in sync
with the OpenAPI specification.

## Directory Independence

This directory is completely self-contained and independent. It does not share
or reuse any code or models from other directories. Please note that:

- Changes in other components won't affect the models in this directory.
- Any updates to SBI APIs or models in other locations won't impact the compatibility of these models.
- The models can be maintained and versioned independently.

## How to Regenerate Models

Make sure you have Node.js, `npx` and `openapi-generator-cli` installed.

To regenerate nfConfig client and models after updating `webconsole-api.yaml`, run:

```
npx openapi-generator-cli generate \
  -i ./nfConfigApi/webconsole-api.yaml \
  -g go \
  -o ./nfConfigApi \
  --additional-properties=packageName=nfConfigApi,validateRequired=true,enumClassPrefix=true
```

To format the code after generating the nfConfig client and models, run:

```
gofumpt -l -w ./nfConfigApi
```

Finally, to restore the required header in .go files, run:

```
sed -i '1i\
// SPDX-FileCopyrightText: 2025 Canonical Ltd\
//\
// SPDX-License-Identifier: Apache-2.0\
//\
' ./nfConfigApi/*.go
```
