import 'dart:io';

/// Windows 开机自启动:HKCU Run 键写/删本程序路径,免管理员、免第三方依赖。
/// 状态判定只看 reg query 的退出码,不解析中文系统本地化输出。
class Autostart {
  static const _valueName = 'ModelSurgeUpstreamAdmin';
  static const _key = r'HKCU\Software\Microsoft\Windows\CurrentVersion\Run';

  static Future<bool> isEnabled() async {
    final r = await Process.run('reg', ['query', _key, '/v', _valueName]);
    return r.exitCode == 0;
  }

  static Future<void> setEnabled(bool enabled) async {
    if (enabled) {
      // 路径含空格,整串加引号
      await Process.run('reg', [
        'add', _key, '/v', _valueName, '/t', 'REG_SZ',
        '/d', '"${Platform.resolvedExecutable}"', '/f',
      ]);
    } else {
      // 不存在时 delete 退出码为 1,忽略
      await Process.run('reg', ['delete', _key, '/v', _valueName, '/f']);
    }
  }
}
