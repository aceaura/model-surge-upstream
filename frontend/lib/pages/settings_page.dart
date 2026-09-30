import 'package:flutter/material.dart';

import '../api_client.dart';
import '../settings_store.dart';
import '../theme.dart';
import '../ui/feedback.dart';

/// 服务地址与管理密钥的设置页。保存前做一次连通性探测，
/// 让运维者在这里就发现地址或密钥错误，而不是回到列表页才看到空白。
/// embedded=true 时作为主壳右侧内容渲染(不带自有 Scaffold/AppBar)。
class SettingsPage extends StatefulWidget {
  const SettingsPage({
    super.key,
    required this.initial,
    required this.onSaved,
    this.dismissible = true,
    this.embedded = false,
  });

  final Settings initial;
  final Future<void> Function(Settings) onSaved;
  final bool dismissible;
  final bool embedded;

  @override
  State<SettingsPage> createState() => _SettingsPageState();
}

class _SettingsPageState extends State<SettingsPage> {
  late final TextEditingController _baseUrl = TextEditingController(
    text: widget.initial.baseUrl,
  );
  late final TextEditingController _adminKey = TextEditingController(
    text: widget.initial.adminKey,
  );

  bool _busy = false;
  bool _revealKey = false;
  Object? _error;
  String? _probeResult;

  @override
  void initState() {
    super.initState();
    // 按钮状态依赖表单内容(已连接 ⇔ 草稿等于已保存配置),监听输入刷新
    _baseUrl.addListener(_refresh);
    _adminKey.addListener(_refresh);
  }

  void _refresh() => setState(() {});

  @override
  void dispose() {
    _baseUrl.dispose();
    _adminKey.dispose();
    super.dispose();
  }

  Settings get _draft =>
      Settings(baseUrl: _baseUrl.text.trim(), adminKey: _adminKey.text.trim());

  /// 草稿与已保存的配置一致即视为已连接:保存前已做过连通性探测,
  /// 且整个应用正基于这份配置运行。改掉任一字段即回到「连接」。
  bool get _connected =>
      widget.initial.complete &&
      _draft.baseUrl == widget.initial.baseUrl &&
      _draft.adminKey == widget.initial.adminKey;

  Future<void> _save() async {
    final draft = _draft;
    if (!draft.complete) {
      setState(
        () => _error = const ValidationException(
          'invalid_request',
          '服务地址与管理密钥都不能为空',
          400,
        ),
      );
      return;
    }
    setState(() {
      _busy = true;
      _error = null;
      _probeResult = null;
    });
    final client = ApiClient(baseUrl: draft.baseUrl, adminKey: draft.adminKey);
    try {
      final providers = await client.listProviders();
      await widget.onSaved(draft);
      if (!mounted) return;
      setState(() => _probeResult = '连接成功，内置 provider ${providers.length} 个');
    } catch (e) {
      if (!mounted) return;
      setState(() => _error = e);
    } finally {
      client.close();
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final onboarding = !widget.initial.complete;
    final body = _body(context, onboarding);
    if (widget.embedded) return body;
    return Scaffold(
      appBar: AppBar(
        title: const Text('连接设置'),
        automaticallyImplyLeading: widget.dismissible,
      ),
      body: body,
    );
  }

  Widget _body(BuildContext context, bool onboarding) {
    final t = context.tokens;
    final messages = <Widget>[
      if (_probeResult != null)
        Padding(
          padding: const EdgeInsets.only(top: 16),
          child: Text(_probeResult!, style: TextStyle(color: t.success)),
        ),
      if (_error != null)
        Padding(
          padding: const EdgeInsets.only(top: 16),
          child: SelectableText(
            describeError(_error!),
            style: TextStyle(color: t.danger),
          ),
        ),
    ];
    final fields = Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        TextField(
          controller: _baseUrl,
          decoration: const InputDecoration(
            labelText: '服务地址',
            hintText: 'http://127.0.0.1:8080',
            border: OutlineInputBorder(),
          ),
        ),
        const SizedBox(height: 16),
        TextField(
          controller: _adminKey,
          obscureText: !_revealKey,
          decoration: InputDecoration(
            labelText: '管理密钥',
            border: const OutlineInputBorder(),
            suffixIcon: IconButton(
              tooltip: _revealKey ? '隐藏' : '显示',
              icon: Icon(_revealKey ? Icons.visibility_off : Icons.visibility),
              onPressed: () => setState(() => _revealKey = !_revealKey),
            ),
          ),
        ),
        const SizedBox(height: 22),
        if (_connected)
          FilledButton.icon(
            onPressed: null,
            // 禁用态默认是灰底,但「已连接」是好消息,
            // 用成功色表达状态而不是"不可用"。
            style: FilledButton.styleFrom(
              disabledBackgroundColor: t.success,
              disabledForegroundColor: Colors.white,
            ),
            icon: const Icon(Icons.check_circle_outline, size: 16),
            label: const Text('已连接'),
          )
        else
          BusyButton(busy: _busy, onPressed: _save, child: const Text('连接')),
      ],
    );
    // 嵌入设置枢纽的可折叠分栏时:不出卡片与滚动,直接铺内容列,
    // 容器与滚动由 CollapsibleSection 和枢纽页提供。
    if (widget.embedded) {
      return Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [fields, ...messages],
      );
    }
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 560),
        child: SingleChildScrollView(
          padding: const EdgeInsets.all(24),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              if (onboarding)
                const Padding(
                  padding: EdgeInsets.only(bottom: 16),
                  child: Text('首次使用请先填写配置中心地址与管理密钥。'),
                ),
              Card(
                child: Padding(
                  padding: const EdgeInsets.all(22),
                  child: fields,
                ),
              ),
              ...messages,
            ],
          ),
        ),
      ),
    );
  }
}
