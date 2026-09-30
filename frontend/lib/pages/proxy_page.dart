import 'dart:io';
import 'dart:math';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api_client.dart';
import '../theme.dart';
import '../ui/feedback.dart';

/// 代理转发面配置：独立端口、独立密钥，把命名模型按各协议原生形态
/// 暴露给客户端（不做协议转化）。改动点「应用配置」后服务端立即重绑监听。
/// 页面本体不带页头——在设置枢纽页里作为「代理服务」分页签渲染。
/// 版式对齐 CC Switch 设置页：分区纵向铺排（粗标题+灰说明+控件），
/// 开关为通栏行（左标题说明、右 Switch），行间细分隔线，不用卡片。
class ProxyPage extends StatefulWidget {
  const ProxyPage({super.key, required this.client, this.onOpenSettings});

  final ApiClient client;
  final VoidCallback? onOpenSettings;

  @override
  State<ProxyPage> createState() => _ProxyPageState();
}

class _ProxyPageState extends State<ProxyPage> {
  final _apiKey = TextEditingController();
  final _port = TextEditingController();

  bool _lanOpen = false;
  bool _revealKey = false;
  bool _loading = true;
  bool _busy = false;
  Object? _error;
  String? _appliedMsg;

  /// 接入地址区里展示的主机：关局域网恒为 127.0.0.1；开局域网时为
  /// 服务端对局域网可见的地址（推导规则见 _resolveDisplayHost）。
  String _displayHost = '127.0.0.1';

  @override
  void initState() {
    super.initState();
    // 端口输入联动接入地址区的展示。
    _port.addListener(() => setState(() {}));
    _load();
  }

  @override
  void dispose() {
    _apiKey.dispose();
    _port.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final s = await widget.client.getProxySettings();
      if (!mounted) return;
      setState(() {
        _apiKey.text = s.apiKey;
        _port.text = '${s.port}';
        _lanOpen = s.lanOpen;
        _loading = false;
      });
      await _resolveDisplayHost();
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e;
        _loading = false;
      });
    }
  }

  /// 生成随机密钥：sk- 前缀 + 48 位十六进制，与常规 sk-xxx 形态的
  /// API 密钥一致，客户端可直接拷贝。
  void _generateKey() {
    final rand = Random.secure();
    final hex = List.generate(
      24,
      (_) => rand.nextInt(256).toRadixString(16).padLeft(2, '0'),
    ).join();
    setState(() {
      _apiKey.text = 'sk-$hex';
      _appliedMsg = null;
    });
  }

  /// 推导接入地址区的主机。关局域网：恒 127.0.0.1。开局域网：
  /// 管理端经局域网地址/域名连的服务端，直接用它（别的设备也这么到）；
  /// 管理端走回环说明服务端就在本机，取本机第一个非回环 IPv4。
  Future<void> _resolveDisplayHost() async {
    if (!_lanOpen) {
      setState(() => _displayHost = '127.0.0.1');
      return;
    }
    final host = Uri.tryParse(widget.client.baseUrl)?.host ?? '';
    if (host.isNotEmpty &&
        host != '127.0.0.1' &&
        host != 'localhost' &&
        host != '::1') {
      setState(() => _displayHost = host);
      return;
    }
    try {
      final ifs = await NetworkInterface.list(type: InternetAddressType.IPv4);
      String? best;
      var bestScore = 0;
      for (final i in ifs) {
        if (_isVirtualInterface(i.name)) continue;
        for (final a in i.addresses) {
          if (a.isLoopback) continue;
          final score = _lanScore(a.address);
          if (score > bestScore) {
            best = a.address;
            bestScore = score;
          }
        }
      }
      if (best != null && mounted) {
        setState(() => _displayHost = best!);
      }
    } catch (_) {
      // 取不到就停在 127.0.0.1，地址区降级为占位展示。
    }
  }

  /// 虚拟网卡按名字排除：它们的地址别的设备永远到不了。
  /// 覆盖 Hyper-V/WSL/docker 的 vEthernet、TUN 代理（Mihomo/Clash）、
  /// VMware/VirtualBox 与各类 TAP。
  bool _isVirtualInterface(String name) {
    final n = name.toLowerCase();
    const patterns = [
      'vethernet',
      'hyper-v',
      'wsl',
      'docker',
      'mihomo',
      'clash',
      'tun',
      'tap',
      'vmware',
      'virtualbox',
      'loopback',
    ];
    return patterns.any(n.contains);
  }

  /// 局域网地址打分：TUN 代理的 198.18/15 与 link-local 不可用记 0 分；
  /// 172.16/12 多为容器/虚拟网段压低；真实物理网段（192.168/16、10/8）优先。
  int _lanScore(String ip) {
    final parts = ip.split('.');
    if (parts.length != 4) return 0;
    final p0 = int.tryParse(parts[0]) ?? 0;
    final p1 = int.tryParse(parts[1]) ?? 0;
    if (p0 == 192 && p1 == 168) return 4;
    if (p0 == 10) return 3;
    if (p0 == 172 && p1 >= 16 && p1 <= 31) return 2;
    if (p0 == 198 && (p1 == 18 || p1 == 19)) return 0; // TUN 代理常用
    if (p0 == 169 && p1 == 254) return 0; // link-local
    return 1;
  }

  /// 复制接入地址到剪贴板，SnackBar 回显所抄内容便于确认没抄错行。
  Future<void> _copyEndpoint(String url) async {
    await Clipboard.setData(ClipboardData(text: url));
    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(content: Text('已复制 $url')),
    );
  }

  Future<void> _apply() async {
    final port = int.tryParse(_port.text.trim()) ?? 0;
    if (port < 1 || port > 65535) {
      setState(
        () => _error = const ValidationException(
          'invalid_request',
          '端口须在 1-65535 之间',
          400,
        ),
      );
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
      _appliedMsg = null;
    });
    try {
      final s = await widget.client.updateProxySettings(
        apiKey: _apiKey.text.trim(),
        port: port,
        lanOpen: _lanOpen,
      );
      if (!mounted) return;
      setState(() {
        _appliedMsg = s.apiKey.isEmpty
            ? '已应用：密钥为空，代理服务已关闭'
            : '已应用：代理服务监听 ${s.lanOpen ? '0.0.0.0' : '127.0.0.1'}:${s.port}';
      });
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) => _body(context);

  String get _portText =>
      _port.text.trim().isEmpty ? '12344' : _port.text.trim();

  Widget _body(BuildContext context) {
    if (_loading) return const Center(child: CircularProgressIndicator());
    if (_error != null && _apiKey.text.isEmpty && _port.text.isEmpty) {
      return ErrorPanel(
        error: _error!,
        onRetry: _load,
        onOpenSettings: widget.onOpenSettings,
      );
    }
    final t = context.tokens;
    // 本页只作为设置枢纽的可折叠分栏内容渲染,滚动交给枢纽页,
    // 这里只出内容列(内边距由 CollapsibleSection 提供)。
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        _section(
          context,
          '代理 API 密钥',
          '客户端（如 Cursor 或 VS Code）必须在 Authorization 头中包含此密钥，留空则关闭代理服务',
          Row(
            children: [
              Expanded(
                child: TextField(
                  controller: _apiKey,
                  obscureText: !_revealKey,
                  decoration: InputDecoration(
                    border: const OutlineInputBorder(),
                    isDense: true,
                    suffixIcon: IconButton(
                      tooltip: _revealKey ? '隐藏' : '显示',
                      icon: Icon(
                        _revealKey ? Icons.visibility_off : Icons.visibility,
                      ),
                      onPressed: () => setState(() => _revealKey = !_revealKey),
                    ),
                  ),
                ),
              ),
              const SizedBox(width: 10),
              OutlinedButton.icon(
                onPressed: _generateKey,
                icon: const Icon(Icons.autorenew, size: 16),
                label: const Text('生成'),
              ),
            ],
          ),
        ),
        const SizedBox(height: 26),
        _section(
          context,
          '服务器端口',
          '代理服务独立监听的端口，与管理面端口互不影响',
          SizedBox(
            width: 220,
            child: TextField(
              controller: _port,
              keyboardType: TextInputType.number,
              inputFormatters: [FilteringTextInputFormatter.digitsOnly],
              decoration: const InputDecoration(
                border: OutlineInputBorder(),
                isDense: true,
              ),
            ),
          ),
        ),
        const SizedBox(height: 18),
        _lanRow(context),
        const SizedBox(height: 26),
        _section(
          context,
          '客户端接入地址',
          _lanOpen ? '局域网设备使用以下地址访问' : '仅本机可访问',
          Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              for (final (label, path) in [
                ('Anthropic 协议', '/anthropic'),
                ('OpenAI 协议', '/openai'),
                ('Gemini 协议', '/gemini'),
              ])
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 5),
                  child: Row(
                    children: [
                      SizedBox(
                        width: 110,
                        child: Text(
                          label,
                          style: TextStyle(fontSize: 12.5, color: t.dim),
                        ),
                      ),
                      Expanded(
                        child: SelectableText(
                          'http://$_displayHost:$_portText$path',
                          style: TextStyle(
                            fontSize: 12.5,
                            fontFamily: 'monospace',
                            color: t.ink,
                          ),
                        ),
                      ),
                      IconButton(
                        tooltip: '复制地址',
                        icon: Icon(Icons.content_copy,
                            size: 15, color: t.faint),
                        splashRadius: 16,
                        visualDensity: VisualDensity.compact,
                        onPressed: () => _copyEndpoint(
                            'http://$_displayHost:$_portText$path'),
                      ),
                    ],
                  ),
                ),
            ],
          ),
        ),
        const SizedBox(height: 26),
        Divider(height: 1, color: t.border),
        const SizedBox(height: 18),
        Row(
          children: [
            Expanded(
              child: _appliedMsg != null
                  ? Text(
                      _appliedMsg!,
                      style: TextStyle(fontSize: 12.5, color: t.success),
                    )
                  : _error != null
                  ? SelectableText(
                      describeError(_error!),
                      style: TextStyle(fontSize: 12.5, color: t.danger),
                    )
                  : const SizedBox.shrink(),
            ),
            const SizedBox(width: 12),
            BusyButton(
              busy: _busy,
              onPressed: _apply,
              child: const Text('应用配置'),
            ),
          ],
        ),
      ],
    );
  }

  /// 分区头：粗标题 + 灰说明，控件跟在下方。对齐 CC Switch 设置页分区。
  Widget _section(
    BuildContext context,
    String title,
    String desc,
    Widget control,
  ) {
    final t = context.tokens;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          title,
          style: TextStyle(
            fontSize: 14,
            fontWeight: FontWeight.w600,
            color: t.ink,
          ),
        ),
        const SizedBox(height: 4),
        Text(desc, style: TextStyle(fontSize: 12, color: t.faint)),
        const SizedBox(height: 12),
        control,
      ],
    );
  }

  /// 开关通栏行：标题/说明 + 右 Switch，上下细分隔线。
  Widget _lanRow(BuildContext context) {
    final t = context.tokens;
    return Column(
      children: [
        Divider(height: 1, color: t.border),
        Padding(
          padding: const EdgeInsets.symmetric(vertical: 14),
          child: Row(
            children: [
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      '开放局域网访问',
                      style: TextStyle(
                        fontSize: 13.5,
                        fontWeight: FontWeight.w600,
                        color: t.ink,
                      ),
                    ),
                    const SizedBox(height: 3),
                    Text(
                      _lanOpen
                          ? '监听 0.0.0.0，局域网内其他设备可访问'
                          : '仅监听 127.0.0.1，仅本机可访问',
                      style: TextStyle(fontSize: 12, color: t.faint),
                    ),
                  ],
                ),
              ),
              Switch(
                value: _lanOpen,
                onChanged: (v) {
                  setState(() {
                    _lanOpen = v;
                    _appliedMsg = null;
                  });
                  _resolveDisplayHost();
                },
              ),
            ],
          ),
        ),
        Divider(height: 1, color: t.border),
      ],
    );
  }
}
