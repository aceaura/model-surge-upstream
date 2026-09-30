import 'dart:math';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api_client.dart';
import '../theme.dart';
import '../ui/feedback.dart';
import '../ui/page_header.dart';

/// 代理转发面配置页：独立端口、独立密钥，把命名模型按各协议原生形态
/// 暴露给客户端（不做协议转化）。改动点「应用配置」后服务端立即重绑监听。
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

  @override
  void initState() {
    super.initState();
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
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e;
        _loading = false;
      });
    }
  }

  /// 生成随机密钥：msu-proxy- 前缀 + 24 位十六进制，客户端可直接拷贝。
  void _generateKey() {
    final rand = Random.secure();
    final hex =
        List.generate(12, (_) => rand.nextInt(256).toRadixString(16).padLeft(2, '0'))
            .join();
    setState(() {
      _apiKey.text = 'msu-proxy-$hex';
      _appliedMsg = null;
    });
  }

  Future<void> _apply() async {
    final port = int.tryParse(_port.text.trim()) ?? 0;
    if (port < 1 || port > 65535) {
      setState(() => _error =
          const ValidationException('invalid_request', '端口须在 1-65535 之间', 400));
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
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        const PageHeader(title: '代理服务', trailing: []),
        Expanded(child: _body(context)),
      ],
    );
  }

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
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 640),
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(24),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text(
                '把命名模型按各协议原生形态暴露给客户端，多协议共用一个端口出口；'
                '不做协议之间的转化，模型协议与入口不匹配会直接报错。',
                style: TextStyle(fontSize: 12.5, color: t.faint),
              ),
              const SizedBox(height: 16),
              _keyCard(context),
              const SizedBox(height: 14),
              _portCard(context),
              const SizedBox(height: 14),
              _lanCard(context),
              const SizedBox(height: 14),
              _endpointCard(context),
              const SizedBox(height: 20),
              BusyButton(
                busy: _busy,
                onPressed: _apply,
                child: const Text('应用配置'),
              ),
              if (_appliedMsg != null)
                Padding(
                  padding: const EdgeInsets.only(top: 14),
                  child: Text(_appliedMsg!, style: TextStyle(color: t.success)),
                ),
              if (_error != null)
                Padding(
                  padding: const EdgeInsets.only(top: 14),
                  child: SelectableText(
                    describeError(_error!),
                    style: TextStyle(color: t.danger),
                  ),
                ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _keyCard(BuildContext context) {
    final t = context.tokens;
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(22),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Row(
              children: [
                Text(
                  '代理 API 密钥',
                  style: TextStyle(
                    fontSize: 14,
                    fontWeight: FontWeight.w600,
                    color: t.ink,
                  ),
                ),
                const Spacer(),
                InkWell(
                  borderRadius: BorderRadius.circular(6),
                  onTap: () => setState(() => _revealKey = !_revealKey),
                  child: Padding(
                    padding:
                        const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                    child: Text(
                      _revealKey ? '隐藏' : '显示 真实值',
                      style: TextStyle(fontSize: 12, color: t.faint),
                    ),
                  ),
                ),
              ],
            ),
            const SizedBox(height: 12),
            Row(
              children: [
                Expanded(
                  child: TextField(
                    controller: _apiKey,
                    obscureText: !_revealKey,
                    decoration: const InputDecoration(
                      hintText: '留空则关闭代理服务',
                      border: OutlineInputBorder(),
                      isDense: true,
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
            const SizedBox(height: 10),
            Text(
              '客户端（如 Cursor 或 VS Code）必须在 Authorization 头中包含此密钥',
              style: TextStyle(fontSize: 12, color: t.faint),
            ),
          ],
        ),
      ),
    );
  }

  Widget _portCard(BuildContext context) {
    final t = context.tokens;
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(22),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text(
              '服务器端口',
              style: TextStyle(
                fontSize: 14,
                fontWeight: FontWeight.w600,
                color: t.ink,
              ),
            ),
            const SizedBox(height: 12),
            TextField(
              controller: _port,
              keyboardType: TextInputType.number,
              inputFormatters: [FilteringTextInputFormatter.digitsOnly],
              decoration: const InputDecoration(
                border: OutlineInputBorder(),
                isDense: true,
              ),
            ),
            const SizedBox(height: 10),
            Text(
              '代理服务独立监听的端口，与管理面端口互不影响',
              style: TextStyle(fontSize: 12, color: t.faint),
            ),
          ],
        ),
      ),
    );
  }

  Widget _lanCard(BuildContext context) {
    final t = context.tokens;
    return Card(
      child: SwitchListTile(
        contentPadding: const EdgeInsets.symmetric(horizontal: 22, vertical: 6),
        title: Text(
          '开放局域网访问',
          style: TextStyle(
            fontSize: 14,
            fontWeight: FontWeight.w600,
            color: t.ink,
          ),
        ),
        subtitle: Text(
          _lanOpen ? '监听 0.0.0.0，局域网内其他设备可访问' : '仅监听 127.0.0.1，仅本机可访问',
          style: TextStyle(fontSize: 12, color: t.faint),
        ),
        value: _lanOpen,
        onChanged: (v) => setState(() {
          _lanOpen = v;
          _appliedMsg = null;
        }),
      ),
    );
  }

  /// 三协议接入地址提示。端口随输入联动，便于填完后直接拷给客户端。
  Widget _endpointCard(BuildContext context) {
    final t = context.tokens;
    final port = _port.text.trim().isEmpty ? '12344' : _port.text.trim();
    const host = '127.0.0.1';
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(22),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Text(
              '客户端接入地址',
              style: TextStyle(
                fontSize: 14,
                fontWeight: FontWeight.w600,
                color: t.ink,
              ),
            ),
            const SizedBox(height: 12),
            for (final (label, path) in [
              ('Anthropic 协议', '/anthropic'),
              ('OpenAI 协议', '/openai'),
              ('Gemini 协议', '/gemini'),
            ])
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 3),
                child: Row(
                  children: [
                    SizedBox(
                      width: 108,
                      child: Text(label,
                          style: TextStyle(fontSize: 12.5, color: t.dim)),
                    ),
                    Expanded(
                      child: SelectableText(
                        'http://$host:$port$path',
                        style: TextStyle(
                          fontSize: 12.5,
                          fontFamily: 'monospace',
                          color: t.ink,
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            const SizedBox(height: 8),
            Text(
              '开放局域网后把 127.0.0.1 换成本机局域网 IP',
              style: TextStyle(fontSize: 12, color: t.faint),
            ),
          ],
        ),
      ),
    );
  }
}
