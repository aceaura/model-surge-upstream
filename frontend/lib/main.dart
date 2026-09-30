import 'package:flutter/material.dart';
import 'package:window_manager/window_manager.dart';

import 'api_client.dart';
import 'models.dart';
import 'pages/accounts_page.dart';
import 'pages/models_page.dart';
import 'pages/providers_page.dart';
import 'pages/settings_page.dart';
import 'settings_store.dart';
import 'theme.dart';

/// 应用版本号(侧栏展示;发版时与 pubspec version 同步)。
const kAppVersion = '1.0.0';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  await windowManager.ensureInitialized();
  await windowManager.waitUntilReadyToShow(
    const WindowOptions(
      size: Size(1180, 760),
      minimumSize: Size(900, 600),
      title: 'ModelSurge Upstream 配置中心',
      titleBarStyle: TitleBarStyle.normal,
    ),
    () async {
      await windowManager.show();
      await windowManager.focus();
    },
  );
  runApp(const AdminApp());
}

class AdminApp extends StatelessWidget {
  const AdminApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'ModelSurge Upstream 配置中心',
      debugShowCheckedModeBanner: false,
      theme: buildAppTheme(),
      darkTheme: buildAppDarkTheme(),
      home: const AdminShell(),
    );
  }
}

/// 应用外壳:左侧固定侧边栏(品牌头 + 分组导航 + 底部连接信息),右侧页面区。
/// 未完成初始配置时整页显示连接设置(无侧栏)。
class AdminShell extends StatefulWidget {
  const AdminShell({super.key, this.store});

  final SettingsStore? store;

  @override
  State<AdminShell> createState() => _AdminShellState();
}

class _AdminShellState extends State<AdminShell> {
  late final SettingsStore _store = widget.store ?? SettingsStore();

  Settings? _settings;
  ApiClient? _client;
  String _page = 'accounts';

  /// 非空时右侧显示该账号的内嵌模型页(账号页的子页,侧栏保持可见)。
  (Account, List<ProviderSpec>)? _modelsCtx;

  @override
  void initState() {
    super.initState();
    _restore();
  }

  Future<void> _restore() async {
    final settings = await _store.load();
    if (!mounted) return;
    setState(() {
      _settings = settings;
      _client = settings.complete ? _clientFor(settings) : null;
    });
  }

  ApiClient _clientFor(Settings s) =>
      ApiClient(baseUrl: s.baseUrl, adminKey: s.adminKey);

  Future<void> _apply(Settings s) async {
    await _store.save(s);
    if (!mounted) return;
    setState(() {
      _client?.close();
      _settings = s;
      _client = _clientFor(s);
      _modelsCtx = null; // 连接已换,子页里的账号快照作废
    });
  }

  /// 错误面板里的「打开设置」:切到设置导航页,不再推路由。
  void _openSettings() => setState(() {
        _page = 'settings';
        _modelsCtx = null;
      });

  void _onNav(String id) => setState(() {
        _page = id;
        _modelsCtx = null;
      });

  @override
  void dispose() {
    _client?.close();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final settings = _settings;
    if (settings == null) {
      return const Scaffold(body: Center(child: CircularProgressIndicator()));
    }
    if (!settings.complete || _client == null) {
      return SettingsPage(
        initial: settings,
        onSaved: _apply,
        dismissible: false,
      );
    }

    final client = _client!;
    final items = <_NavItem>[
      _NavItem(
        'accounts',
        Icons.account_circle_outlined,
        '账号',
        () => AccountsPage(
          client: client,
          onOpenSettings: _openSettings,
          onOpenModels: (account, providers) =>
              setState(() => _modelsCtx = (account, providers)),
        ),
      ),
      _NavItem(
        'providers',
        Icons.dns_outlined,
        '提供商',
        () => ProvidersPage(client: client, onOpenSettings: _openSettings),
      ),
      _NavItem(
        'settings',
        Icons.settings_outlined,
        '连接设置',
        () => SettingsPage(initial: settings, onSaved: _apply, embedded: true),
      ),
    ];
    final groups = [
      _NavGroup('管理', items.sublist(0, 2)),
      _NavGroup('系统', items.sublist(2)),
    ];
    final cur = items.firstWhere((i) => i.id == _page, orElse: () => items[0]);
    final modelsCtx = _modelsCtx;

    return Scaffold(
      body: Row(
        children: [
          _sidebar(context.tokens, groups, cur.id, settings),
          // client 更换(保存设置)后强制重建页面,避免列表页持有旧连接。
          Expanded(
            // 内容区底色:亮模式纯白(CC Switch 式干净底色),暗模式跟随 tokens
            child: Container(
              color: Theme.of(context).brightness == Brightness.light
                  ? Colors.white
                  : context.tokens.bg,
              child: SafeArea(
                child: KeyedSubtree(
                  key: ObjectKey(client),
                  child: modelsCtx != null
                      ? ModelsPage(
                          client: client,
                          account: modelsCtx.$1,
                          providers: modelsCtx.$2,
                          onOpenSettings: _openSettings,
                          onBack: () => setState(() => _modelsCtx = null),
                        )
                      : cur.build(),
                ),
              ),
            ),
          ),
        ],
      ),
    );
  }

  Widget _sidebar(
    AppTokens t,
    List<_NavGroup> groups,
    String currentId,
    Settings settings,
  ) {
    return Container(
      width: 236,
      decoration: BoxDecoration(
        color: t.surface,
        border: Border(right: BorderSide(color: t.border)),
      ),
      padding: const EdgeInsets.fromLTRB(14, 20, 14, 14),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(10, 2, 10, 16),
            child: Row(
              children: [
                ClipRRect(
                  borderRadius: BorderRadius.circular(10),
                  child: Image.asset(
                    'assets/logo.png',
                    width: 36,
                    height: 36,
                    fit: BoxFit.cover,
                  ),
                ),
                const SizedBox(width: 11),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        'ModelSurge',
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(
                          fontSize: 15,
                          fontWeight: FontWeight.w700,
                          color: t.ink,
                        ),
                      ),
                      Text(
                        'Upstream 配置中心 v$kAppVersion',
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(fontSize: 11, color: t.faint),
                      ),
                    ],
                  ),
                ),
              ],
            ),
          ),
          Expanded(
            child: ListView(
              padding: EdgeInsets.zero,
              children: [
                for (final g in groups) ...[
                  if (g.section.isNotEmpty) _SectionLabel(g.section),
                  for (final it in g.items) _navItem(t, it, currentId == it.id),
                  const SizedBox(height: 10),
                ],
              ],
            ),
          ),
        ],
      ),
    );
  }

  Widget _navItem(AppTokens t, _NavItem item, bool on) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 1),
      child: InkWell(
        borderRadius: BorderRadius.circular(10),
        onTap: () => _onNav(item.id),
        child: Container(
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
          decoration: BoxDecoration(
            color: on ? t.primarySoft : Colors.transparent,
            borderRadius: BorderRadius.circular(10),
          ),
          child: Row(
            children: [
              Icon(item.icon, size: 17, color: on ? t.primaryInk : t.faint),
              const SizedBox(width: 11),
              Text(
                item.label,
                style: TextStyle(
                  fontSize: 13.5,
                  fontWeight: on ? FontWeight.w600 : FontWeight.w500,
                  color: on ? t.primaryInk : t.dim,
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _NavGroup {
  final String section;
  final List<_NavItem> items;
  const _NavGroup(this.section, this.items);
}

class _NavItem {
  final String id;
  final IconData icon;
  final String label;
  final Widget Function() build;
  const _NavItem(this.id, this.icon, this.label, this.build);
}

/// 侧栏节标题:主色指示条 + 加粗墨色 + 延伸分隔线。
class _SectionLabel extends StatelessWidget {
  final String text;
  const _SectionLabel(this.text);

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return Padding(
      padding: const EdgeInsets.fromLTRB(12, 14, 12, 8),
      child: Row(
        children: [
          Container(
            width: 3,
            height: 13,
            decoration: BoxDecoration(
              color: t.primary,
              borderRadius: BorderRadius.circular(2),
            ),
          ),
          const SizedBox(width: 7),
          Text(
            text,
            style: TextStyle(
              fontSize: 12.5,
              fontWeight: FontWeight.w700,
              letterSpacing: 1.5,
              color: t.ink,
            ),
          ),
          const SizedBox(width: 10),
          Expanded(child: Container(height: 1, color: t.border)),
        ],
      ),
    );
  }
}
