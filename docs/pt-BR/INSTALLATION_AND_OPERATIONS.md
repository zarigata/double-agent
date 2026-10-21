# Instalação e operação

## 1. Experiência desejada

O usuário executa uma linha em um host Linux limpo:

```bash
curl -fsSL https://<dominio-oficial>/install.sh | sudo bash
```

O domínio é propositalmente um placeholder. Nenhum instalador deve ser
publicado antes de existir release assinado e documentação de segurança.

O script é somente um bootstrap:

1. detecta distribuição, arquitetura e virtualização;
2. mostra o que será instalado e pede confirmação;
3. adiciona repositório assinado ou baixa pacote com checksum/assinatura;
4. instala `double-agent`, unidade systemd e dependências selecionadas;
5. executa setup interativo no TTY;
6. configura firewall apenas para a sub-rede escolhida;
7. inicia o daemon;
8. executa diagnóstico e mostra URL/fingerprint.

Para ambientes que não aceitam `curl | bash`, oferecer `.deb`, `.rpm`, arquivo
tar assinado e instruções equivalentes.

## 2. Host suportado no MVP

Recomendação inicial:

- Ubuntu Server 24.04 LTS x86_64;
- systemd e cgroups v2;
- 4 cores, 16 GiB RAM e 100 GiB livres mínimos recomendados;
- Docker Engine;
- QEMU/KVM + libvirt para VMs;
- rede local com mDNS ou conexão manual por IP/DNS;
- GPU opcional.

Debian e outras distribuições entram após estabilizar o primeiro vertical slice.
ARM64 pode executar CPU/containers, mas precisa de templates próprios e não deve
ser prometido no primeiro release sem testes.

## 3. Setup interativo

Perguntas:

1. nome do servidor;
2. interface/sub-rede LAN permitida;
3. usuário administrador;
4. senha e confirmação, lidas sem eco;
5. diretório/volume de dados;
6. quotas reservadas ao host;
7. habilitar Docker;
8. habilitar libvirt/KVM;
9. política de atualização;
10. telemetria — desabilitada por padrão;
11. IA interna agora, depois ou nunca.

O instalador não pergunta chave de API em argumento de shell. Segredos são
adicionados depois na Web UI por conexão TLS.

## 4. Drivers de GPU

O instalador não deve substituir silenciosamente drivers funcionais. Fluxo:

1. detectar PCI IDs, kernel modules e runtimes;
2. executar probes sem modificar;
3. classificar cada GPU como `ready`, `driver_missing`, `runtime_missing`,
   `unsupported` ou `needs_reboot`;
4. oferecer comando/ação específica para o host;
5. pedir autorização antes de instalar driver/runtime;
6. reiniciar somente com confirmação;
7. executar smoke-test em container.

### NVIDIA

Validar driver no host e NVIDIA Container Toolkit/CDI. O daemon deve selecionar
dispositivo individual, não expor `all` por padrão.

### AMD

Validar suporte ROCm da GPU/kernel, `/dev/kfd`, render node e grupos. Nem toda
GPU AMD suporta toda versão ROCm; capabilities devem refletir o teste real.

### Intel

Validar render node `/dev/dri`, driver kernel, Level Zero/OpenCL/OpenVINO e
acesso do usuário de serviço. O primeiro servidor Intel define a matriz concreta
de versões.

## 5. Serviços

```text
double-agent.service        daemon e Web UI
double-agent-gc.timer       coleta de instâncias/artefatos expirados
double-agent-update.timer   apenas checagem; instalação depende da política
```

Docker/libvirt continuam serviços separados. O daemon usa APIs locais com
permissão mínima possível.

## 6. Portas e nomes

| Uso | Padrão proposto | Exposição |
|---|---:|---|
| Web UI/API/MCP HTTPS | 7443/TCP | LAN escolhida |
| mDNS | 5353/UDP | LAN/multicast |
| VNC/RDP do guest | dinâmica/interna | nunca diretamente na LAN |
| Guest agent | vsock/virtio/private | host/guest somente |

O usuário pode fornecer DNS normal. `.local` depende de mDNS e pode conflitar
com políticas da rede; o CLI sempre aceita IP/hostname manual.

## 7. Certificados e pairing

No bootstrap:

- gerar CA local e certificado do servidor;
- incluir IPs/hostnames conhecidos como SAN;
- mostrar SHA-256 fingerprint;
- bridge faz pairing com código curto e fixa a CA/fingerprint;
- navegador recebe instrução para confiar na CA ou usa reverse proxy com
  certificado administrado pelo usuário.

Nunca oferecer botão “ignorar TLS” permanente.

## 8. CLI administrativa

```text
double-agent status
double-agent doctor [--json]
double-agent discover
double-agent pair <server>
double-agent connect <client>
double-agent users ...
double-agent keys ...
double-agent templates ...
double-agent instances ...
double-agent jobs ...
double-agent artifacts ...
double-agent backup
double-agent restore
double-agent update
double-agent uninstall
double-agent mcp-bridge ...
```

Comandos mutáveis mostram alvo e exigem confirmação quando usados por humano;
`--yes` existe para automação autenticada.

## 9. Operação diária

### Dashboard

- CPU/RAM/disco e reserva do host;
- uso/temperatura/VRAM por GPU quando exposto pelo driver;
- jobs em fila e ativos;
- instâncias e TTL;
- cache e artefatos;
- saúde Docker/libvirt;
- falhas que exigem ação.

### Alertas locais

- disco ou VRAM em pressão;
- runtime indisponível;
- instância que não encerra;
- falha repetida de template;
- certificado próximo da expiração;
- update disponível;
- login ou pairing suspeito.

Notificações externas ficam opcionais e posteriores.

## 10. Backup e restauração

Backup padrão inclui:

- SQLite consistente;
- configuração;
- CA e chaves necessárias cifradas;
- templates e receitas;
- opcionalmente artefatos.

Não inclui por padrão:

- instâncias;
- overlays;
- imagens OCI reconstruíveis;
- modelos reconstruíveis;
- logs volumosos.

O backup deve ser cifrado e restaurável em um host sem GPU. Após restore,
dispositivos são redetectados e receitas podem resolver para hardware diferente.

## 11. Atualização e rollback

- canais `stable` e futuramente `preview`;
- baixar e verificar antes de parar;
- backup automático de banco/config;
- migração transacional;
- health check após troca;
- rollback de binário e schema quando suportado;
- templates atualizados independentemente do daemon;
- nunca atualizar driver GPU automaticamente junto do daemon.

## 12. Desinstalação

`double-agent uninstall`:

1. lista instâncias e jobs afetados;
2. para e destrói recursos após confirmação;
3. remove serviços e binários;
4. pergunta separadamente se deve preservar `/var/lib/double-agent`;
5. nunca remove Docker/libvirt/driver que já existiam antes;
6. gera relatório do que permaneceu.

## 13. Kubernetes

Kubernetes não será instalado pelo one-liner do MVP. O caminho futuro:

- controlador Double Agent fora ou dentro do cluster;
- node agent/DaemonSet para inventário;
- Device Plugins/CDI para GPUs;
- Jobs/Pods para containers;
- KubeVirt para VMs quando escolhido;
- PersistentVolume para cache/artefatos;
- mesmas APIs MCP e receitas.

Isso evita impor a complexidade operacional de cluster ao usuário de um único
servidor.

