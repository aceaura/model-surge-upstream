import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';
import '../ui/confirm_dialog.dart';
import '../ui/dialog_band.dart';
import '../ui/feedback.dart';
import '../ui/page_header.dart';
import '../ui/styled_dropdown.dart';

/// 对话页：左侧会话列表（新对话/切换/删除），右侧消息流 + 模型选择 + 输入框。
/// 补全走服务端：按所选模型的出站协议透传上游，整段历史落库可回看。
class ChatPage extends StatefulWidget {
  const ChatPage({super.key, required this.client, this.onOpenSettings});

  final ApiClient client;
  final VoidCallback? onOpenSettings;

  @override
  State<ChatPage> createState() => _ChatPageState();
}

class _ChatPageState extends State<ChatPage> {
  final TextEditingController _input = TextEditingController();
  final FocusNode _inputFocus = FocusNode();
  final ScrollController _scroll = ScrollController();

  List<ChatSession> _sessions = [];
  List<ChatMessage> _messages = [];
  List<UpstreamModel> _models = [];
  String? _sessionId;
  String? _modelId;
  bool _loadingSessions = true;
  bool _sending = false;
  Object? _error;

  @override
  void initState() {
    super.initState();
    _inputFocus.onKeyEvent = _onInputKey;
    _load();
  }

  @override
  void dispose() {
    _input.dispose();
    _inputFocus.dispose();
    _scroll.dispose();
    super.dispose();
  }

  /// Enter 发送、Shift+Enter 换行：与常见聊天客户端一致。
  KeyEventResult _onInputKey(FocusNode node, KeyEvent event) {
    if (event is! KeyDownEvent ||
        (event.logicalKey != LogicalKeyboardKey.enter &&
            event.logicalKey != LogicalKeyboardKey.numpadEnter)) {
      return KeyEventResult.ignored;
    }
    final pressed = HardwareKeyboard.instance.logicalKeysPressed;
    final shifted = pressed.contains(LogicalKeyboardKey.shiftLeft) ||
        pressed.contains(LogicalKeyboardKey.shiftRight);
    if (shifted) return KeyEventResult.ignored;
    _send();
    return KeyEventResult.handled;
  }

  Future<void> _load() async {
    setState(() {
      _loadingSessions = true;
      _error = null;
    });
    try {
      final sessions = await widget.client.listChatSessions();
      final models = await widget.client.listModels();
      if (!mounted) return;
      setState(() {
        _sessions = sessions;
        _models = models.where((m) => m.enabled).toList();
        _loadingSessions = false;
      });
      if (_sessionId == null && _sessions.isNotEmpty) {
        await _select(_sessions.first.id);
      } else if (_sessionId != null) {
        await _select(_sessionId!);
      }
      _syncModelChoice();
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e;
        _loadingSessions = false;
      });
    }
  }

  /// 模型选择回显：会话最近用过的模型优先，否则第一个可用模型。
  void _syncModelChoice() {
    if (_models.isEmpty) {
      return;
    }
    final ids = _models.map((m) => m.id).toList();
    final sess = _sessions.where((s) => s.id == _sessionId).firstOrNull;
    if (_modelId == null || !ids.contains(_modelId)) {
      _modelId = (sess != null && ids.contains(sess.modelId))
          ? sess.modelId
          : ids.first;
    }
  }

  Future<void> _select(String id) async {
    setState(() => _sessionId = id);
    try {
      final (sess, msgs) = await widget.client.getChatMessages(id);
      if (!mounted) return;
      setState(() {
        _messages = msgs;
        if (sess.modelId.isNotEmpty) _modelId = sess.modelId;
      });
      _syncModelChoice();
      _toEnd();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _newSession() async {
    try {
      final s = await widget.client.createChatSession();
      if (!mounted) return;
      setState(() {
        _sessions = [s, ..._sessions];
        _messages = [];
        _sessionId = s.id;
      });
      _syncModelChoice();
      _inputFocus.requestFocus();
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _deleteSession(ChatSession s) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => ConfirmDialog(
        title: '删除会话',
        message: '将删除「${s.title}」及其全部消息，不可恢复。',
      ),
    );
    if (ok != true) return;
    try {
      await widget.client.deleteChatSession(s.id);
      if (!mounted) return;
      setState(() {
        _sessions = _sessions.where((x) => x.id != s.id).toList();
        if (_sessionId == s.id) {
          _sessionId = _sessions.isNotEmpty ? _sessions.first.id : null;
          _messages = [];
        }
      });
      if (_sessionId != null) await _select(_sessionId!);
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _clearMessages() async {
    final id = _sessionId;
    if (id == null) return;
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => const ConfirmDialog(
        title: '清除消息',
        message: '将清除当前会话的全部消息，会话本身保留。',
        confirmLabel: '清除',
        severity: ConfirmSeverity.warning,
      ),
    );
    if (ok != true) return;
    try {
      await widget.client.clearChatMessages(id);
      if (!mounted) return;
      setState(() => _messages = []);
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  /// 重命名会话(KiroaaS 行内铅笔)：弹窗预填现标题，提交后原地替换列表项。
  Future<void> _renameSession(ChatSession s) async {
    final title = await showDialog<String>(
      context: context,
      builder: (ctx) => _RenameDialog(initial: s.title),
    );
    if (title == null || title.isEmpty || title == s.title) return;
    try {
      final updated = await widget.client.renameChatSession(s.id, title);
      if (!mounted) return;
      setState(() {
        _sessions = [
          for (final x in _sessions)
            if (x.id == updated.id) updated else x
        ];
      });
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _send() async {
    final text = _input.text.trim();
    if (text.isEmpty || _sending) return;
    final model = _modelId;
    if (model == null) {
      showError(context, '没有可用模型：先到模型页启用一个模型');
      return;
    }
    var id = _sessionId;
    if (id == null) {
      try {
        final s = await widget.client.createChatSession();
        if (!mounted) return;
        setState(() {
          _sessions = [s, ..._sessions];
          _sessionId = s.id;
        });
        id = s.id;
      } catch (e) {
        if (mounted) showError(context, e);
        return;
      }
    }
    setState(() {
      _sending = true;
      _input.clear();
    });
    try {
      final msgs = await widget.client.sendChatMessage(id,
          modelId: model, content: text);
      if (!mounted) return;
      final sessions = await widget.client.listChatSessions();
      if (!mounted) return;
      setState(() {
        _messages = msgs;
        _sessions = sessions;
        _sending = false;
      });
      _toEnd();
      _inputFocus.requestFocus();
    } catch (e) {
      if (!mounted) return;
      setState(() => _sending = false);
      showError(context, e);
      // 用户消息已落库：失败也重拉消息与会话，让发出去的话和自动标题
      // 同步上屏，避免界面上「看不到自己发了什么」。
      await _select(id);
      try {
        final sessions = await widget.client.listChatSessions();
        if (mounted) setState(() => _sessions = sessions);
      } catch (_) {
        // 会话列表刷新失败不叠加第二个错误提示。
      }
      // 输入还回去：失败不该让用户重打一遍。
      _input.text = text;
      _input.selection =
          TextSelection.collapsed(offset: _input.text.length);
      _inputFocus.requestFocus();
    }
  }

  void _toEnd() {
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (!mounted || !_scroll.hasClients) return;
      _scroll.jumpTo(_scroll.position.maxScrollExtent);
    });
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    if (_loadingSessions) {
      return const Center(child: CircularProgressIndicator());
    }
    if (_error != null) {
      return ErrorPanel(
        error: _error!,
        onRetry: _load,
        onOpenSettings: widget.onOpenSettings,
      );
    }
    return Row(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        _sessionPane(t),
        VerticalDivider(width: 1, thickness: 1, color: t.border),
        Expanded(child: _chatPane(t)),
      ],
    );
  }

  Widget _sessionPane(AppTokens t) {
    return SizedBox(
      width: 250,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(18, 18, 18, 10),
            child: Row(
              children: [
                Text('对话',
                    style: TextStyle(
                        fontSize: 15, fontWeight: FontWeight.w600, color: t.ink)),
                const Spacer(),
                Text('${_sessions.length} 个',
                    style: TextStyle(fontSize: 12, color: t.faint)),
              ],
            ),
          ),
          Padding(
            padding: const EdgeInsets.fromLTRB(14, 0, 14, 10),
            child: OutlinedButton.icon(
              onPressed: _newSession,
              icon: const Icon(Icons.add_rounded, size: 15),
              label: const Text('新对话'),
            ),
          ),
          Expanded(
            child: ListView(
              padding: const EdgeInsets.fromLTRB(14, 0, 14, 14),
              children: [
                for (final s in _sessions)
                  _SessionRow(
                    t: t,
                    session: s,
                    selected: s.id == _sessionId,
                    onSelect: () => _select(s.id),
                    onRename: () => _renameSession(s),
                    onDelete: () => _deleteSession(s),
                  ),
              ],
            ),
          ),
        ],
      ),
    );
  }

  Widget _chatPane(AppTokens t) {
    final sess = _sessions.where((s) => s.id == _sessionId).firstOrNull;
    if (sess == null) {
      return Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.chat_bubble_outline_rounded, size: 42, color: t.faint),
            const SizedBox(height: 12),
            Text('暂无对话，开一个新会话试试',
                style: TextStyle(fontSize: 13, color: t.faint)),
            const SizedBox(height: 16),
            FilledButton(onPressed: _newSession, child: const Text('新对话')),
          ],
        ),
      );
    }
    return Column(
      children: [
        PageHeader(
          title: sess.title.isEmpty ? '新对话' : sess.title,
          // 清除=清空当前会话消息(KiroaaS 右上「清除」);删除会话移到侧栏行内垃圾桶。
          trailing: [
            OutlinedButton(
              onPressed: _messages.isEmpty ? null : _clearMessages,
              child: const Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(Icons.delete_sweep_outlined, size: 15),
                  SizedBox(width: 6),
                  Text('清除'),
                ],
              ),
            ),
          ],
        ),
        const Divider(height: 1),
        Expanded(
          child: ListView(
            controller: _scroll,
            padding: const EdgeInsets.fromLTRB(24, 20, 24, 12),
            children: [
              for (final m in _messages) _bubble(t, m),
              if (_sending) _thinking(t),
            ],
          ),
        ),
        _inputBar(t),
      ],
    );
  }

  Widget _bubble(AppTokens t, ChatMessage m) {
    final isUser = m.role == 'user';
    final bubble = Container(
      margin: const EdgeInsets.only(bottom: 14),
      constraints: const BoxConstraints(maxWidth: 620),
      padding: const EdgeInsets.fromLTRB(14, 10, 14, 10),
      decoration: BoxDecoration(
        color: isUser ? t.primarySoft : t.surface,
        border: Border.all(color: isUser ? Colors.transparent : t.border),
        borderRadius: BorderRadius.only(
          topLeft: const Radius.circular(12),
          topRight: const Radius.circular(12),
          bottomLeft: Radius.circular(isUser ? 12 : 4),
          bottomRight: Radius.circular(isUser ? 4 : 12),
        ),
      ),
      child: SelectableText(
        m.content,
        style: TextStyle(
          fontSize: 13.5,
          height: 1.6,
          color: isUser ? t.primaryInk : t.ink,
        ),
      ),
    );
    return Align(
      alignment: isUser ? Alignment.centerRight : Alignment.centerLeft,
      child: bubble,
    );
  }

  Widget _thinking(AppTokens t) {
    return Align(
      alignment: Alignment.centerLeft,
      child: Container(
        margin: const EdgeInsets.only(bottom: 14),
        padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
        decoration: BoxDecoration(
          color: t.surface,
          border: Border.all(color: t.border),
          borderRadius: const BorderRadius.only(
            topLeft: Radius.circular(12),
            topRight: Radius.circular(12),
            bottomLeft: Radius.circular(4),
            bottomRight: Radius.circular(12),
          ),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            SizedBox(
              width: 13,
              height: 13,
              child: CircularProgressIndicator(strokeWidth: 2, color: t.primary),
            ),
            const SizedBox(width: 9),
            Text('思考中…', style: TextStyle(fontSize: 12, color: t.faint)),
          ],
        ),
      ),
    );
  }

  Widget _inputBar(AppTokens t) {
    // KiroaaS 式输入卡：圆角描边容器内，模型选择收成左上小胶囊，
    // 输入框去边框贴底，发送键为圆形图标钮。
    return Padding(
      padding: const EdgeInsets.fromLTRB(20, 4, 20, 16),
      child: Container(
        decoration: BoxDecoration(
          color: t.surface,
          border: Border.all(color: t.border),
          borderRadius: BorderRadius.circular(14),
        ),
        padding: const EdgeInsets.all(12),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Align(
              alignment: Alignment.centerLeft,
              child: SizedBox(
                width: 220,
                child: StyledDropdown(
                  value: _modelId,
                  options: _models.map((m) => m.id).toList(),
                  showValue: _modelId != null,
                  dropUp: true,
                  decoration: InputDecoration(
                    isDense: true,
                    filled: true,
                    fillColor: t.bg,
                    contentPadding:
                        const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
                    border: OutlineInputBorder(
                      borderRadius: BorderRadius.circular(16),
                      borderSide: BorderSide.none,
                    ),
                  ),
                  onChanged: (v) => setState(() => _modelId = v),
                ),
              ),
            ),
            const SizedBox(height: 6),
            Row(
              crossAxisAlignment: CrossAxisAlignment.end,
              children: [
                Expanded(
                  child: TextField(
                    controller: _input,
                    focusNode: _inputFocus,
                    minLines: 1,
                    maxLines: 5,
                    textInputAction: TextInputAction.newline,
                    decoration: const InputDecoration(
                      border: InputBorder.none,
                      isCollapsed: true,
                      hintText: '输入消息… Enter 发送，Shift+Enter 换行',
                    ),
                  ),
                ),
                const SizedBox(width: 8),
                BusyButton(
                  busy: _sending,
                  onPressed: _send,
                  style: FilledButton.styleFrom(
                    minimumSize: const Size(34, 34),
                    padding: EdgeInsets.zero,
                    shape: const CircleBorder(),
                  ),
                  child: const Icon(Icons.send_rounded, size: 16),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

/// 重命名会话弹窗：控制器归弹窗自身 State 所有，
/// 关闭动画期间字段仍在构建，提前 dispose 会撞「used after being disposed」。
/// 校验通过以修剪后的标题 pop，取消 pop null。
class _RenameDialog extends StatefulWidget {
  const _RenameDialog({required this.initial});

  final String initial;

  @override
  State<_RenameDialog> createState() => _RenameDialogState();
}

class _RenameDialogState extends State<_RenameDialog> {
  late final TextEditingController _controller =
      TextEditingController(text: widget.initial);
  final _formKey = GlobalKey<FormState>();

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  void _submit(BuildContext ctx) {
    if (!(_formKey.currentState?.validate() ?? false)) return;
    Navigator.pop(ctx, _controller.text.trim());
  }

  @override
  Widget build(BuildContext context) {
    final t = context.tokens;
    return AlertDialog(
      // 常规级(非破坏):与确认弹窗同构的色带头,只换中性配色。
      clipBehavior: Clip.antiAlias,
      titlePadding: EdgeInsets.zero,
      contentPadding: const EdgeInsets.fromLTRB(22, 16, 22, 4),
      actionsPadding: const EdgeInsets.fromLTRB(22, 14, 22, 17),
      title: DialogBand(
        title: '重命名会话',
        icon: Icons.edit_outlined,
        background: t.bg,
        foreground: t.dim,
      ),
      content: SizedBox(
        width: 420,
        child: Form(
          key: _formKey,
          child: TextFormField(
            controller: _controller,
            autofocus: true,
            decoration: const InputDecoration(
              labelText: '会话标题',
              border: OutlineInputBorder(),
            ),
            validator: (v) =>
                (v == null || v.trim().isEmpty) ? '标题不能为空' : null,
            onFieldSubmitted: (_) => _submit(context),
          ),
        ),
      ),
      actions: [
        OutlinedButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('取消')),
        FilledButton(
          onPressed: () => _submit(context),
          child: const Text('保存'),
        ),
      ],
    );
  }
}

/// 会话行：标题 + 时间，右侧行内重命名/删除(仿 KiroaaS)。
/// 动作悬停显现；选中行常显，保证不悬停也找得到入口。
class _SessionRow extends StatefulWidget {
  const _SessionRow({
    required this.t,
    required this.session,
    required this.selected,
    required this.onSelect,
    required this.onRename,
    required this.onDelete,
  });

  final AppTokens t;
  final ChatSession session;
  final bool selected;
  final VoidCallback onSelect;
  final VoidCallback onRename;
  final VoidCallback onDelete;

  @override
  State<_SessionRow> createState() => _SessionRowState();
}

class _SessionRowState extends State<_SessionRow> {
  bool _hovered = false;

  Widget _action(IconData icon, String tooltip, VoidCallback onPressed) {
    return IconButton(
      tooltip: tooltip,
      icon: Icon(icon, size: 15, color: widget.t.faint),
      splashRadius: 15,
      visualDensity: VisualDensity.compact,
      padding: EdgeInsets.zero,
      constraints: const BoxConstraints(minWidth: 26, minHeight: 26),
      onPressed: onPressed,
    );
  }

  @override
  Widget build(BuildContext context) {
    final t = widget.t;
    final s = widget.session;
    final d = s.updatedAt.toLocal();
    final sub =
        '${d.month}月${d.day}日 ${d.hour.toString().padLeft(2, '0')}:${d.minute.toString().padLeft(2, '0')}';
    final showActions = _hovered || widget.selected;
    return Container(
      margin: const EdgeInsets.only(bottom: 6),
      decoration: BoxDecoration(
        color: widget.selected ? t.primarySoft : Colors.transparent,
        border: Border.all(color: widget.selected ? t.primary : t.border),
        borderRadius: BorderRadius.circular(10),
      ),
      child: MouseRegion(
        onEnter: (_) => setState(() => _hovered = true),
        onExit: (_) => setState(() => _hovered = false),
        child: InkWell(
          borderRadius: BorderRadius.circular(10),
          onTap: widget.onSelect,
          child: Padding(
            padding: const EdgeInsets.fromLTRB(12, 9, 8, 9),
            child: Row(
              children: [
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        s.title.isEmpty ? '新对话' : s.title,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: TextStyle(
                          fontSize: 13,
                          fontWeight: FontWeight.w600,
                          color: widget.selected ? t.primaryInk : t.ink,
                        ),
                      ),
                      const SizedBox(height: 3),
                      Text(sub, style: TextStyle(fontSize: 11, color: t.faint)),
                    ],
                  ),
                ),
                // 隐藏不从树里摘：悬停进出行高不变，列表不跳。
                Opacity(
                  opacity: showActions ? 1 : 0,
                  child: IgnorePointer(
                    ignoring: !showActions,
                    child: Row(
                      mainAxisSize: MainAxisSize.min,
                      children: [
                        _action(Icons.edit_outlined, '重命名', widget.onRename),
                        _action(
                            Icons.delete_outline, '删除会话', widget.onDelete),
                      ],
                    ),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
