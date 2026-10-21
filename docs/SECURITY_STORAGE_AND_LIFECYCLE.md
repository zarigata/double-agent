# Segurança, persistência e ciclo de vida

## 1. Modelo de confiança

O MVP pressupõe uma LAN administrada, mas não uma LAN honesta. Dispositivos
comprometidos, páginas maliciosas, clientes de IA com prompt injection e código
executado nas instâncias são ameaças reais.

Princípio central:

> Um agente pode controlar completamente a instância que recebeu, mas não o
> host, outras instâncias, dispositivos não alocados, segredos ou sessões de
> outros clientes.

## 2. Fronteiras

```text
[Cliente/LLM não confiável]
          |
      TLS + token
          v
[Gateway e política] ---- [Cofre]
          |
      operação validada
          v
[Runtime host privilegiado]
          |
      isolamento
          v
[Instância não confiável] ---- [Internet/rede conforme política]
```

O plano de controle e o runtime privilegiado são Trusted Computing Base. Web
UI, templates e atualizador também precisam de revisão de supply chain.

## 3. “Acesso a tudo” com limite seguro

Dentro da instância, a chave com `instances:admin` pode:

- usar root/Administrator;
- instalar pacotes;
- iniciar serviços;
- abrir desktop/navegador;
- compilar e executar código;
- acessar GPU alocada;
- apagar a própria instância.

Ela não pode:

- montar filesystem do host;
- usar Docker/libvirt socket diretamente;
- ver `/proc` do host;
- acessar GPU não alocada;
- ler banco ou cofre;
- acessar instância alheia;
- alterar daemon ou firewall;
- persistir fora das áreas de cache/artefato aprovadas.

Um modo de manutenção do host pode existir exclusivamente na Web UI/CLI local
para o administrador humano; ele não deve ser ferramenta MCP geral.

## 4. Autenticação e sessão web

Bootstrap:

1. instalador pergunta username pelo TTY;
2. senha é lida sem eco, confirmada e nunca entra em argumento/env/log;
3. daemon armazena hash Argon2id com parâmetros registrados;
4. primeiro login exige aceitar fingerprint/CA e gera recovery codes;
5. criação de novas contas/chaves passa a ocorrer na Web UI.

Controles web:

- cookie `HttpOnly`, `Secure`, `SameSite=Strict`;
- proteção CSRF;
- expiração e rotação de sessão;
- rate limiting e lockout progressivo;
- opção futura de WebAuthn;
- nenhum segredo devolvido ao browser após gravação;
- CSP rígida e dependências front-end minimizadas.

## 5. Segredos

Categorias:

- chaves de provedores LLM;
- tokens de repositórios/registries;
- credenciais de aplicações;
- chaves de pairing e certificados;
- licenças privadas.

Armazenamento:

- envelope encryption com chave mestra local;
- chave de dados por segredo;
- chave mestra fora do SQLite, arquivo root-only no MVP;
- suporte futuro a TPM/KMS/Vault;
- referências opacas como `secret://openai/main`;
- redaction centralizada em logs, erros e auditoria;
- valor montado/injetado somente na instância e pelo tempo necessário;
- rotação e revogação registradas.

Estar na mesma rede não torna aceitável guardar chaves em texto simples.

## 6. Rede

Política padrão:

- Web UI e MCP escutam em interface LAN configurada, nunca abertas
  acidentalmente para WAN;
- firewall permite somente a sub-rede escolhida;
- runtime usa redes privadas por job;
- instâncias não alcançam plano de controle, metadata endpoints ou outras
  instâncias;
- DNS é controlado e logado em nível operacional;
- egress pode ser `none`, `allowlist` ou `internet` por template;
- serviços do guest são acessados somente via gateway autenticado;
- mDNS anuncia metadados mínimos.

Proteções obrigatórias contra DNS rebinding incluem validação de `Origin` e
`Host`. Certificados podem usar CA local gerada no bootstrap; o bridge fixa a
CA/fingerprint no pairing.

## 7. Supply chain

- releases do Double Agent assinados;
- instalador pequeno, legível e fixado por versão;
- digests obrigatórios para imagens de template estáveis;
- SBOM e scanner de vulnerabilidade por release;
- assinatura/verificação de templates oficiais;
- origem e hash de modelos e ISOs;
- sem executar automaticamente scripts de template comunitário como host;
- atualização com preview, rollback e canal estável;
- dependências e licenças inventariadas.

## 8. Persistência

### Mantido

- contas e hashes;
- chaves em forma de hash/metadados;
- segredos cifrados;
- templates instalados;
- receitas resolvidas;
- sessões e jobs;
- eventos de auditoria;
- cache reconstruível;
- artefatos explicitamente exportados.

### Apagado com a instância

- root filesystem/overlay;
- `/tmp` e memória;
- perfil do navegador;
- credenciais transitórias montadas;
- arquivos não exportados;
- tokens de desktop/guest agent.

### Configurável

- logs completos;
- inputs enviados;
- screenshots;
- artefatos finais;
- cache de pacotes/modelos.

## 9. Layout proposto

```text
/etc/double-agent/
  config.yaml
  ca/
/var/lib/double-agent/
  state.db
  secrets/
  templates/
  cache/images/
  cache/models/
  vm-bases/
  artifacts/
  workspaces/
/var/log/double-agent/
  daemon.log
  audit.log
```

Permissões são restritas ao usuário de serviço. Diretórios de runtime podem ser
separados em partição com quota.

## 10. Lifecycle e retenção

Política inicial recomendada:

| Item | Padrão |
|---|---|
| Instância ociosa | parar após 15 minutos |
| Instância parada | destruir após 60 minutos |
| Vida máxima | 8 horas |
| Workspace temporário | apagar com a instância |
| Artefato exportado | 7 dias |
| Logs operacionais | 30 dias |
| Auditoria | 90 dias |
| Cache | LRU por quota, sem TTL fixo |

O administrador altera defaults. Uma chave pode reduzir retenção, não ampliar
além da própria cota.

## 11. Destruição

1. impedir novos comandos;
2. cancelar ou concluir exportações conforme política;
3. desmontar segredos;
4. parar processos com grace period;
5. destruir container/VM e overlay;
6. liberar GPU, rede, portas e leases;
7. apagar tokens transitórios;
8. registrar receita, resultado e evento;
9. reconciliar ausência no runtime.

Apagar arquivo em SSD não garante eliminação física imediata. A documentação
deve distinguir destruição lógica, criptográfica e sanitização de mídia.

## 12. Auditoria

Registrar:

- autenticação, pairing, criação/revogação de chaves;
- criação, execução, desktop, parada e destruição;
- template/digest e recursos alocados;
- uso de referência de segredo, sem valor;
- import/export de artefato e hash;
- mudanças de política;
- atualizações;
- ações da IA interna identificadas como tal.

O audit log é append-only logicamente e encadeado por hash para detectar
alterações acidentais. Exportação para syslog/SIEM fica posterior.

## 13. Ameaças prioritárias

| Ameaça | Mitigação principal |
|---|---|
| Prompt injection pede host escape | Ferramentas não expõem operação host arbitrária |
| Container monta Docker socket | Validador de template e policy hard deny |
| Roubo de token na LAN | TLS, pairing, token curto/rotacionável |
| DNS rebinding | `Origin`/`Host` estritos e autenticação |
| Modelo lê segredo | Referência opaca e injeção fora do contexto |
| Template malicioso | Assinatura, digest, revisão e sandbox |
| Artefato infectado | Hash, MIME, aviso e scanner opcional |
| GPU vazando dados | Reset/limpeza quando suportado, isolamento declarado, modo exclusivo |
| Disco enche | quotas e GC |
| Instância órfã | leases, TTL e reconciliador |
| Bridge altera config errada | preview, backup e confirmação |

## 14. Mudança para internet/comercial

Antes de sair da LAN:

- OAuth 2.1 e identidade externa;
- mTLS entre nós;
- banco e fila adequados a HA;
- isolamento por microVM para workloads hostis;
- rate limiting distribuído;
- egress proxy e política por tenant;
- criptografia/backup gerenciados;
- pentest e resposta a incidentes;
- termos, privacidade e revisão de todas as licenças.

