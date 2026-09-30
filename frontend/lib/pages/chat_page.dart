import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../api_client.dart';
import '../models.dart';
import '../theme.dart';
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
      builder: (ctx) => AlertDialog(
        title: const Text('删除会话'),
        content: Text('将删除「${s.title}」及其全部消息，不可恢复。'),
        actions: [
          TextButton(
              onPressed: () => Navigator.pop(ctx, false),
              child: const Text('取消')),
          FilledButton(
              onPressed: () => Navigator.pop(ctx, true),
              child: const Text('删除')),
        ],
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
      builder: (ctx) => AlertDialog(
        title: const Text('清空消息'),
        content: const Text('将清空当前会话的全部消息，会话本身保留。'),
        actions: [
          TextButton(
              onPressed: () => Navigator.pop(ctx, false),
              child: const Text('取消')),
          FilledButton(
              onPressed: () => Navigator.pop(ctx, true),
              child: const Text('清空')),
        ],
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
              children: [for (final s in _sessions) _sessionRow(t, s)],
            ),
          ),
        ],
      ),
    );
  }

  Widget _sessionRow(AppTokens t, ChatSession s) {
    final on = s.id == _sessionId;
    final d = s.updatedAt.toLocal();
    final sub =
        '${d.month}月${d.day}日 ${d.hour.toString().padLeft(2, '0')}:${d.minute.toString().padLeft(2, '0')}';
    return Container(
      margin: const EdgeInsets.only(bottom: 6),
      decoration: BoxDecoration(
        color: on ? t.primarySoft : Colors.transparent,
        border: Border.all(color: on ? t.primary : t.border),
        borderRadius: BorderRadius.circular(10),
      ),
      child: InkWell(
        borderRadius: BorderRadius.circular(10),
        onTap: () => _select(s.id),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(12, 9, 12, 9),
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
                  color: on ? t.primaryInk : t.ink,
                ),
              ),
              const SizedBox(height: 3),
              Text(sub, style: TextStyle(fontSize: 11, color: t.faint)),
            ],
          ),
        ),
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
          trailing: [
            OutlinedButton(
              onPressed: _messages.isEmpty ? null : _clearMessages,
              child: const Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  Icon(Icons.delete_sweep_outlined, size: 15),
                  SizedBox(width: 6),
                  Text('清空'),
                ],
              ),
            ),
            const SizedBox(width: 8),
            IconButton(
              tooltip: '删除会话',
              icon: Icon(Icons.delete_outline_rounded, size: 18, color: t.dim),
              onPressed: () => _deleteSession(sess),
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
    return Container(
      decoration: BoxDecoration(
        border: Border(top: BorderSide(color: t.border)),
      ),
      padding: const EdgeInsets.fromLTRB(20, 12, 20, 16),
      child: Row(
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
                hintText: '输入消息… Enter 发送，Shift+Enter 换行',
              ),
            ),
          ),
          const SizedBox(width: 10),
          SizedBox(
            width: 220,
            child: StyledDropdown(
              value: _modelId,
              options: _models.map((m) => m.id).toList(),
              showValue: _modelId != null,
              dropUp: true,
              decoration: const InputDecoration(labelText: '模型'),
              onChanged: (v) => setState(() => _modelId = v),
            ),
          ),
          const SizedBox(width: 10),
          BusyButton(
            busy: _sending,
            onPressed: _send,
            child: const Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(Icons.send_rounded, size: 15),
                SizedBox(width: 6),
                Text('发送'),
              ],
            ),
          ),
        ],
      ),
    );
  }
}
