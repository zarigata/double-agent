# Referências

Pesquisa realizada em 21 de outubro de 2026. Priorizar documentação oficial e
fixar versões durante a implementação, pois runtimes, drivers e MCP mudam.

## Inspiração

- [Xiryn — repositório e arquitetura](https://github.com/SouravBeraAkaSpeed/xiryn):
  estação Linux em Docker por agente, desktop remoto e máquinas descartáveis.
  O Double Agent reutiliza a ideia de isolamento/lifecycle, mas é um servidor de
  infraestrutura MCP independente de um agente obrigatório.

## Model Context Protocol

- [Especificação MCP 2026-07-28](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/index.mdx)
- [Streamable HTTP](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/transports/streamable-http.mdx)
- [`server/discover`](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/server/discover.mdx)
- [Descoberta do authorization server](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/basic/authorization/authorization-server-discovery.mdx)
- [MCP Server Cards e AI Catalog](https://github.com/modelcontextprotocol/ext-server-card/blob/main/docs/discovery.md)

Conclusões usadas no plano:

- Streamable HTTP é o transporte remoto preferido;
- o endpoint precisa validar `Origin` e autenticar conexões;
- a versão 2026-07-28 é stateless, portanto jobs usam handles explícitos;
- Server Cards descobrem por domínio, não substituem broadcast mDNS;
- compatibilidade com clientes anteriores precisa de testes e negociação.

## GPU em containers

### NVIDIA

- [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/overview.html)
- [Suporte a CDI](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/cdi-support.html)
- [Guia de instalação](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/install-guide.html)

CDI padroniza a injeção de dispositivos em runtimes compatíveis e permite
selecionar GPUs ou partições suportadas.

### AMD

- [ROCm: instalação em Linux](https://rocm.docs.amd.com/projects/install-on-linux/en/latest/)
- [ROCm em containers](https://rocm.docs.amd.com/projects/install-on-linux/en/latest/how-to/docker.html)

O acesso típico requer os dispositivos `/dev/kfd` e `/dev/dri`; compatibilidade
depende do modelo, kernel e versão ROCm.

### Intel

- [Intel oneAPI containers](https://www.intel.com/content/www/us/en/docs/oneapi/installation-guide-linux/2025-0/run-a-sample-project-using-an-eclipse-container.html)
- [Intel GPU documentation](https://dgpu-docs.intel.com/)
- [OpenVINO GPU device](https://docs.openvino.ai/2025/openvino-workflow/running-inference/inference-devices-and-modes/gpu-device.html)

O acesso em containers normalmente passa pelos render nodes em `/dev/dri` e
backends como Level Zero, OpenCL, OpenVINO ou Vulkan. O MVP deve provar a GPU
Intel concreta em vez de assumir compatibilidade pelo vendor.

## Virtualização e sistemas operacionais

- [libvirt domain XML — host devices](https://libvirt.org/formatdomain.html#host-device-assignment)
- [Linux VFIO](https://docs.kernel.org/driver-api/vfio.html)
- [QEMU documentation](https://www.qemu.org/docs/master/)
- [Apple Virtualization framework](https://developer.apple.com/documentation/virtualization)
- [Apple Software License Agreements](https://www.apple.com/legal/sla/)
- [Microsoft Windows licensing documents](https://www.microsoft.com/licensing/docs/view/Windows-Desktop)

Conclusões usadas:

- passthrough depende de IOMMU, grupos e suporte de reset/hardware;
- macOS virtualizado deve permanecer ligado a hardware/licenças Apple;
- imagens e acesso remoto do Windows possuem obrigações próprias; o usuário
  fornece mídia e licença.

## Licenciamento do produto

- [Open Source Definition](https://opensource.org/osd)
- [FAQ da Open Source Initiative](https://opensource.org/faq)

A definição de open source exige redistribuição livre e não permite proibir
uso comercial. Um software cujo código pode ser visto, mas não revendido ou
redistribuído sem autorização, deve ser descrito como **source-available**, não
open source. A licença final requer aconselhamento jurídico.

## Referências a selecionar durante a implementação

- API oficial do Docker Engine;
- API e bindings libvirt escolhidos;
- especificação OCI e Container Device Interface;
- mDNS/DNS-SD (RFC 6762/6763);
- Argon2 e parâmetros atuais de password hashing;
- OAuth 2.1 e RFCs exigidas pela versão MCP escolhida;
- formatos SPDX/CycloneDX para SBOM;
- SLSA/Sigstore para cadeia de release;
- termos de cada modelo e aplicação distribuídos em templates.

