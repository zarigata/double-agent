# Runtimes, GPUs e templates

## 1. Estratégia de execução

O produto usa a unidade mais leve que atende à tarefa:

| Necessidade | Runtime preferido | Motivo |
|---|---|---|
| Linux, build, IA, Blender | Container | Inicialização rápida e bom acesso à GPU |
| Desktop Linux | Container com desktop web | Menor custo que uma VM |
| Kernel/sistema isolado | VM Linux | Kernel e init próprios |
| Windows | VM QEMU/KVM | Containers Linux não executam Windows |
| Código não confiável em oferta pública | MicroVM futura | Isolamento mais forte |
| macOS | Worker Mac futuro | Requer hardware/ecossistema Apple |

“Mini VM” é o conceito de produto; internamente pode ser container ou VM. A API
expõe capacidade, não obriga o cliente a conhecer o backend.

## 2. Containers Linux

Backend MVP: Docker Engine. A interface de runtime não deve depender de
detalhes exclusivos do Docker para permitir containerd/Podman no futuro.

Controles mínimos:

- CPU quota e cpuset;
- limite de RAM e swap;
- volume raiz efêmero;
- workspace temporário separado;
- sem socket Docker dentro do container;
- capabilities removidas por padrão;
- seccomp/AppArmor;
- rede por job;
- dispositivo GPU explicitamente alocado;
- desktop publicado somente pelo gateway autenticado;
- labels com IDs de job, instância, usuário, template e TTL.

O agente pode ser root dentro do container. `--privileged`, host PID/network,
montagem de `/` e socket Docker são proibidos para templates normais.

## 3. VMs Linux e Windows

Backend MVP: libvirt + QEMU/KVM.

### Linux

- cloud image obtida de fonte permitida;
- cloud-init para guest agent, usuário efêmero e chave de sessão;
- disco overlay qcow2 descartável sobre base somente leitura;
- virtio para disco e rede;
- SPICE/noVNC ou RDP por gateway;
- exportação via guest agent.

### Windows

- ISO/base fornecida pelo administrador;
- instalação automatizada opcional por resposta não secreta;
- licença e ativação são responsabilidade do usuário;
- drivers virtio instalados na base pelo administrador;
- WinRM não é exposto na LAN; guest agent usa canal privado;
- Office e outros softwares só aparecem em templates se o usuário os licenciar;
- disco overlay apagado ao destruir;
- sysprep/base image documentada, mas não redistribuída pelo projeto.

VM Windows CPU faz parte do MVP. GPU por VFIO é experimental e só aparece como
capacidade quando o diagnóstico do host comprova IOMMU, grupo seguro, dispositivo
secundário para o host e reset funcional.

## 4. Inventário de GPU

O inventário normaliza cada dispositivo:

```json
{
  "id": "gpu_01...",
  "vendor": "intel",
  "model": "Intel Arc A770",
  "pci_address": "0000:03:00.0",
  "render_node": "/dev/dri/renderD128",
  "memory_bytes": 17163091968,
  "backends": ["level-zero", "opencl", "vulkan"],
  "runtime_modes": ["container", "vfio"],
  "health": "ready"
}
```

Nunca usar apenas o índice `0`: índices mudam. IDs estáveis derivam de UUID do
vendor ou endereço PCI com fingerprint do host.

## 5. Matriz GPU do MVP

| Vendor | Container Linux | Mecanismo | Validação mínima | VM |
|---|---|---|---|---|
| NVIDIA | Suportado | NVIDIA Container Toolkit/CDI | `nvidia-smi`, CUDA e render Vulkan | VFIO experimental; MIG quando suportado fica posterior |
| AMD | Suportado | `/dev/kfd` e render nodes `/dev/dri` | ROCm/HIP, memória e render | VFIO experimental |
| Intel | Suportado | render nodes `/dev/dri`, Level Zero/OpenCL | `sycl-ls`/Level Zero, OpenVINO e render | VFIO/SR-IOV experimental conforme hardware |
| CPU | Suportado | cgroups | inferência/render funcional | Suportado |

Suporte de vendor não significa que qualquer modelo rode em qualquer placa.
Cada template declara backends aceitos, memória mínima, arquitetura e fallback.

## 6. Compartilhamento e exclusividade

### Containers

Podem compartilhar uma GPU, mas memória de vídeo e interferência de performance
nem sempre são isoladas pelo runtime. O MVP oferece três políticas:

- `exclusive`: um job por GPU;
- `shared`: jobs concorrentes com limite de quantidade e aviso;
- `best_effort`: sem garantia de memória/performance.

O padrão para geração pesada é `exclusive`; desktop/render leve pode usar
`shared` quando validado.

### VMs

PCI passthrough entrega normalmente o dispositivo inteiro e o remove do host.
MIG, mediated devices e SR-IOV dependem do vendor e do modelo. Não serão
mascarados por uma promessa genérica. A capability reportará exatamente o modo
disponível.

## 7. Scheduler de recursos

Uma solicitação declara requisitos, não um PCI address:

```yaml
resources:
  cpu: 8
  memory: 16GiB
  disk: 40GiB
  gpu:
    count: 1
    vendors: [intel, amd, nvidia]
    memory_min: 8GiB
    mode: exclusive
    backends_any: [level-zero, rocm, cuda]
```

O scheduler filtra template + hardware + política e registra a resolução na
receita. Se não houver capacidade, o job aguarda ou falha com alternativas
estruturadas.

## 8. Templates

Template é manifesto assinado e versionado, não uma imagem opaca. Exemplo
conceitual:

```yaml
apiVersion: double-agent/v1alpha1
kind: Template
metadata:
  name: image-generation
  version: 0.1.0
spec:
  runtime: container
  base:
    image: registry.example/double-agent/image-worker@sha256:...
  capabilities:
    - image.generate
  hardware:
    cpuFallback: true
    gpuBackends: [cuda, rocm, level-zero]
  cache:
    - name: models
      mount: /models
      readOnly: true
  inputs:
    prompt: { type: string, required: true }
  outputs:
    - glob: /workspace/output/**
      artifact: true
  lifecycle:
    idleTimeout: 10m
    maxLifetime: 2h
  licenses:
    redistributableImage: true
    userSuppliedModels: true
```

### Templates iniciais

#### `dev-linux`

Ubuntu/Debian, toolchains comuns, shell e upload/clone. Serve builds e testes.

#### `desktop-linux`

Desktop leve, Chromium, terminal e acesso web autenticado. Não salva perfil
depois da destruição salvo exportação explícita.

#### `blender`

Blender headless e desktop, Python API, render EGL/Vulkan quando compatível,
exportação `.blend`, `.glb`, `.fbx`, imagens e logs.

#### `image-generation`

Um frontend inicialmente escolhido após protótipo, com adaptadores CUDA, ROCm,
Intel e CPU. Modelos ficam em cache separado e só são baixados quando licença e
origem forem aprovadas.

#### `windows-test`

VM baseada na imagem fornecida pelo usuário, PowerShell, guest agent, desktop e
exportação de relatórios. Aplicações adicionais são camadas/receitas privadas.

#### `browser-worker`

Chromium automatizável, screenshots, downloads e desktop para takeover.

## 9. Receitas e reprodutibilidade

Ao criar uma instância, o servidor resolve o template numa receita imutável:

- versão e digest da imagem/base;
- recursos efetivamente alocados;
- pacotes e comandos de preparação;
- artefatos de entrada e hashes;
- modelo e licença, quando aplicável;
- variáveis não secretas;
- referências a segredos, nunca seus valores;
- passos executados e códigos de saída;
- artefatos de saída e hashes.

A receita permite reconstruir uma instância equivalente; não promete
reprodução bit a bit quando downloads externos ou hardware variam.

## 10. Cache

Caches persistentes reduzem o custo de ambientes descartáveis:

- imagens OCI;
- bases qcow2;
- modelos de IA;
- pacotes apt/npm/pip, opcionalmente;
- assets comuns.

Cada entrada tem origem, digest, tamanho, licença declarada, último uso e
política de limpeza. Instâncias recebem cache read-only; escrita vai para uma
camada temporária. Conteúdo sem hash ou licença desconhecida não entra no
catálogo compartilhado automaticamente.

## 11. Desktop remoto

O desktop não terá porta VNC/RDP diretamente publicada na LAN. O gateway cria
URL de curta duração, limitada à instância e ao usuário. O usuário pode:

- assistir;
- assumir teclado/mouse;
- pausar a automação;
- devolver controle com uma nota;
- encerrar a sessão.

Clipboard e upload/download são desligados por padrão ou passam pelo sistema de
artefatos para auditoria.

## 12. Diagnóstico do host

`double-agent doctor` deve verificar:

- virtualização CPU e `/dev/kvm`;
- IOMMU e grupos para passthrough;
- Docker, libvirt e espaço;
- driver e runtime NVIDIA;
- `/dev/kfd`, ROCm e grupos para AMD;
- `/dev/dri`, Level Zero/OpenCL e grupos para Intel;
- teste curto por backend;
- portas, mDNS, certificado e resolução `.local`;
- cgroups v2, AppArmor/SELinux e firewall;
- relógio e entropia;
- capacidade de criar, executar e apagar uma instância smoke-test.

O resultado vira capabilities reais do servidor; o instalador não declara GPU
“suportada” apenas porque detectou um PCI ID.

