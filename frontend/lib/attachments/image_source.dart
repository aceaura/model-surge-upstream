import 'dart:typed_data';

import 'package:file_selector/file_selector.dart';
import 'package:pasteboard/pasteboard.dart';

/// 待发图片的张数与单张大小上限，与服务端 validateImages 对齐，
/// 前端先拦一道避免把注定失败的请求发出去。
const maxPendingImages = 4;
const maxImageBytes = 4 << 20;

/// 待发图片：内存字节 + mime，base64 编码推迟到发送时进行。
class PendingImage {
  const PendingImage({required this.mime, required this.bytes});

  final String mime;
  final Uint8List bytes;
}

/// 取图途径抽象（文件选择 / 剪贴板）。插件在 flutter test 里不能真调，
/// widget 测试注入 fake 实现。
abstract class ImageSource {
  /// 弹文件选择器挑图；用户取消返回空列表。
  Future<List<PendingImage>> pickFiles();

  /// 读剪贴板里的图片；剪贴板没有图返回 null（调用方回落文本粘贴）。
  Future<PendingImage?> readClipboard();
}

/// 生产实现：file_selector 选文件 + pasteboard 读剪贴板截图。
class SystemImageSource implements ImageSource {
  static const _extensions = ['png', 'jpg', 'jpeg', 'webp', 'gif'];

  static const _mimes = {
    'png': 'image/png',
    'jpg': 'image/jpeg',
    'jpeg': 'image/jpeg',
    'webp': 'image/webp',
    'gif': 'image/gif',
  };

  @override
  Future<List<PendingImage>> pickFiles() async {
    final files = await openFiles(
      acceptedTypeGroups: [
        const XTypeGroup(label: '图片', extensions: _extensions),
      ],
    );
    final out = <PendingImage>[];
    for (final f in files) {
      // cross_file 这一版没有 getMimeType，且选择器已按扩展名过滤，
      // mime 直接由扩展名映射。
      final ext = f.name.split('.').last.toLowerCase();
      final mime = _mimes[ext];
      if (mime == null) continue;
      out.add(PendingImage(mime: mime, bytes: await f.readAsBytes()));
    }
    return out;
  }

  @override
  Future<PendingImage?> readClipboard() async {
    // pasteboard 读出的剪贴板图统一是 PNG 字节。
    final bytes = await Pasteboard.image;
    if (bytes == null || bytes.isEmpty) return null;
    return PendingImage(mime: 'image/png', bytes: bytes);
  }
}
