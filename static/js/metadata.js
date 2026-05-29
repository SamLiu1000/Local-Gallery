/* ============================================================
   metadata.js - 通用 AI 图片元数据解析器 v2.1chy
   
   设计理念：
   不依赖特定 AI 工具（A1111/ComfyUI/NovelAI/Flux 等）的硬编码格式，
   而是采用通用方法：
   1. 从图片二进制中提取所有文本块（PNG tEXt/iTXt/zTXt, JPEG EXIF, WebP）
   2. 将原始数据聚合为统一的数据池
   3. 通过通用语义识别引擎分析数据：
      - 自然语言文本 → 提示词
      - Key:Value 模式 → 生成参数
      - JSON 深度遍历 → 递归提取所有可识别参数
   4. 通过参数名标准化映射表统一输出格式
   
   这样无论什么 AI 工具生成的图片，只要元数据中包含参数信息，
   都能被正确解析出来。
   
   v2.1 更新：
   - 修复 iTXt chunk 解析：正确处理 compression_flag 和多余 null 字节
   - 修复 zTXt chunk 解析：使用浏览器原生 DecompressionStream API 解压
   - 增强 iTXt 压缩数据处理：支持 zlib 解压
   - 改进 SD WebUI Forge 新版 iTXt 格式兼容性
   ============================================================ */

const MetadataParser = (() => {

    // ==================== 参数名标准化映射表 ====================
    // 将各种 AI 工具的不同参数名统一映射到标准名称
    // 格式: 标准名 -> [可能的原始名称列表]
    // 匹配时忽略大小写，支持部分匹配

    const PARAM_ALIASES = {
        'Steps': [
            'steps', 'step', 'num_steps', 'num_inference_steps', 'Steps',
            'sampling_steps', 'n_steps', 'iterations', 'Steps:'
        ],
        'CFG Scale': [
            'cfg', 'cfg_scale', 'cfg scale', 'guidance', 'guidance_scale',
            'classifier_free_guidance', 'CFG Scale', 'cfg-scale',
            'cfgScale', 'CFGScale', 'guidanceScale'
        ],
        'Sampler': [
            'sampler', 'sampler_name', 'samplerName', 'sample', 'sample_method',
            'sampling_method', 'scheduler_type', 'Sampler', 'sampling'
        ],
        'Scheduler': [
            'scheduler', 'scheduler_name', 'schedulerName', 'noise_scheduler',
            'Scheduler', 'beta_schedule'
        ],
        'Seed': [
            'seed', 'Seed', 'noise_seed', 'random_seed', 'rand_seed',
            'initial_seed', 'global_seed'
        ],
        'Size': [
            'size', 'Size', 'resolution', 'image_size', 'output_size',
            'dimensions', 'width_x_height'
        ],
        'Width': [
            'width', 'w', 'image_width', 'img_width', 'output_width',
            'Width', 'W', 'latent_width'
        ],
        'Height': [
            'height', 'h', 'image_height', 'img_height', 'output_height',
            'Height', 'H', 'latent_height'
        ],
        'Model hash': [
            'model hash', 'model_hash', 'modelHash', 'Model hash',
            'checkpoint_hash', 'ckpt_hash', 'sd_hash', 'sd_model_hash',
            'model_sha256', 'checkpoint_sha256'
        ],
        'Model': [
            'model', 'model_name', 'modelName', 'checkpoint', 'ckpt_name',
            'ckpt', 'base_model', 'sd_model', 'sd_checkpoint', 'unet_name',
            'diffusion_model', 'pretrained_model', 'Model', 'model_id',
            'checkpoint_name'
        ],
        'VAE': [
            'vae', 'vae_name', 'vaeName', 'VAE', 'vae_model',
            'vae_checkpoint'
        ],
        'CLIP': [
            'clip', 'clip_name', 'clipName', 'CLIP', 'text_encoder',
            'clip_model', 'clip_skip', 'clipSkip', 'clip_layer'
        ],
        'LoRA': [
            'lora', 'lora_name', 'loraName', 'LoRA', 'lora_model',
            'lora_weight', 'lora_strength', 'lycoris', 'locon',
            'lora_hashes', 'lora_hash'
        ],
        'ControlNet': [
            'controlnet', 'control_net', 'controlNet', 'ControlNet',
            'cn_model', 'cn_type', 'control_type'
        ],
        'Denoise': [
            'denoise', 'denoising', 'denoising_strength', 'Denoise',
            'denoise_strength'
        ],
        'Batch Size': [
            'batch', 'batch_size', 'batchSize', 'n_iter', 'batch_count',
            'total_batch'
        ],
        'Prompt': [
            'prompt', 'positive', 'pos', 'text', 'caption', 'description',
            'input_text', 'positive_prompt', 'pos_prompt'
        ],
        'Negative Prompt': [
            'negative', 'neg', 'negative_prompt', 'neg_prompt', 'uc',
            'unconditioned', 'negativePrompt', 'negative text'
        ],
        'Upscaler': [
            'upscaler', 'upscale', 'upscale_model', 'upscaler_name',
            'Upscaler', 'hr_upscaler'
        ],
        'Hires Fix': [
            'hires', 'hires_fix', 'hiresFix', 'highres', 'highres_fix',
            'enable_hr', 'hires_upscale', 'hires_steps'
        ],
        'Clip Skip': [
            'clip_skip', 'clipSkip', 'clip_layer', 'clip_stop_at_last_layers'
        ],
        'ENSD': [
            'ensd', 'eta_noise_seed_delta', 'ENSD'
        ],
        'Token Merging': [
            'token_merging', 'tokenMerging', 'tome', 'to_me_ratio'
        ],
        'Refiner': [
            'refiner', 'refiner_model', 'refinerName', 'refiner_switch_at'
        ],
        'Flux Guidance': [
            'flux_guidance', 'fluxGuidance', 'flux_guidance_scale'
        ],
        'Schedule type': [
            'schedule type', 'schedule_type', 'scheduleType', 'Schedule type',
            'schedule_type_name', 'noise_schedule'
        ],
        'Distilled CFG Scale': [
            'distilled cfg scale', 'distilled_cfg_scale', 'distilledCFGScale',
            'Distilled CFG Scale', 'distilled_guidance_scale'
        ],
        'Flux Guidance': [
            'flux_guidance', 'fluxGuidance', 'flux_guidance_scale',
            'guidance', 'Flux Guidance'
        ],
        'Axios': [
            'axios', 'Axios'
        ],
        'Version': [
            'version', 'Version', 'app_version', 'sd_version',
            'sd_version_name', 'forge_version', 'webui_version'
        ],
        'Hypernet': [
            'hypernet', 'hyper_net', 'hypernetwork', 'Hypernet'
        ],
        'ADetailer': [
            'adetailer', 'ADetailer', 'ad_model', 'ad_prompt'
        ],
        'Face Restoration': [
            'face_restoration', 'face_restore', 'face_restorer',
            'codeformer', 'gfpgan', 'restore_faces'
        ],
        'Style': [
            'style', 'Style', 'style_name', 'style_preset'
        ],
    };

    // 已知的非参数字段（软件信息、时间戳等），这些不应该被当作参数
    // 注意：'model' 和 'description' 在 AI 生成上下文中是重要参数，已移除
    const NON_PARAM_KEYS = new Set([
        'software', 'Software', 'source', 'Source', 'creation time',
        'Creation Time', 'creator', 'Creator', 'title', 'Title',
        'comment', 'Comment',
        'copyright', 'Copyright', 'make', 'Make',
        'datetime', 'DateTime', 'exif', 'Exif', 'xmp', 'XMP',
        'xmlns', 'rdf', 'dc', 'photoshop', 'adobe', 'xml',
        'sui_image_params', 'workflow',  // 这些是容器键，不是参数本身
        // ComfyUI / 工作流 UI 噪声字段
        'id', 'revision', 'last_node_id', 'last_link_id', 'order', 'mode',
        'properties', 'widgets_values', 'pos', 'size', 'flags', 'outputs', 'inputs',
        'links', 'slot_index', 'shape', 'color', 'bgcolor', 'collapsed',
        'horizontal', 'shownav', 'showallgraphs', 'showoutputtext',
        'toggleRestriction', 'directory', 'url', 'cnr_id', 'ver',
        'node name for s&r', 'models', 'link', 'offset', 'font_size',
        'scale', 'dir', 'type', 'class_type'
    ]);

    const COMFY_NOISE_PATH_PARTS = new Set([
        'id', 'revision', 'last_node_id', 'last_link_id',
        'pos', 'size', 'flags', 'order', 'mode', 'properties',
        'outputs', 'output', 'inputs', 'links', 'link', 'slot_index',
        'collapsed', 'horizontal', 'shownav', 'showallgraphs', 'showoutputtext',
        'toggleRestriction', 'directory', 'url', 'cnr_id', 'ver',
        'node name for s&r', 'models', 'color', 'bgcolor', 'offset',
        'font_size', 'scale', 'dir', 'shape'
    ]);

    // ==================== 主解析入口 ====================

    /**
     * 从 File 对象解析元数据
     * @param {File} file
     * @returns {Promise<Object>} { prompt, negativePrompt, params: {...}, raw: {...} }
     */
    async function parseFile(file) {
        const buffer = await file.arrayBuffer();
        const ext = file.name.split('.').pop().toLowerCase();
        return parseBufferInternal(buffer, ext);
    }

    /**
     * 从 Blob/ArrayBuffer 解析元数据
     */
    async function parseBuffer(buffer, extension) {
        const ext = (extension || 'png').toLowerCase();
        return parseBufferInternal(buffer, ext);
    }

    function parseBufferInternal(buffer, ext) {
        if (ext === 'png') return parsePNG(buffer);
        if (ext === 'jpg' || ext === 'jpeg') return parseJPEG(buffer);
        if (ext === 'webp') return parseWebP(buffer);
        return createEmptyResult();
    }

    // ==================== zlib 解压工具 ====================

    /**
     * 使用浏览器原生 DecompressionStream API 解压 zlib 数据
     * 如果浏览器不支持，回退到尝试使用全局 pako 库
     * @param {Uint8Array} data - zlib 压缩的数据（含 2 字节 zlib header）
     * @returns {Promise<Uint8Array>} 解压后的数据
     */
    async function inflateZlib(data) {
        console.log('[Metadata] inflateZlib: 开始解压, 数据长度:', data.length, '前2字节:', data[0]?.toString(16), data[1]?.toString(16));
        
        // 方法1: 使用浏览器原生 DecompressionStream API
        if (typeof DecompressionStream !== 'undefined') {
            try {
                // 先尝试 'deflate-raw'（去掉 zlib header）
                const ds = new DecompressionStream('deflate-raw');
                // zlib 格式有 2 字节 header 和 4 字节 checksum，需要去掉
                // deflate-raw 只需要压缩数据部分（去掉 zlib header 2 字节和尾部 checksum 4 字节）
                let rawData = data;
                if (data.length > 2 && (data[0] === 0x78)) {
                    // 有 zlib header，去掉 header（2字节）和尾部 adler32（4字节）
                    rawData = data.slice(2, data.length - 4);
                }
                console.log('[Metadata] inflateZlib: 使用 deflate-raw, rawData长度:', rawData.length);
                const writer = ds.writable.getWriter();
                writer.write(rawData);
                writer.close();
                const reader = ds.readable.getReader();
                const chunks = [];
                while (true) {
                    const { done, value } = await reader.read();
                    if (done) break;
                    chunks.push(value);
                }
                const totalLen = chunks.reduce((sum, c) => sum + c.length, 0);
                const result = new Uint8Array(totalLen);
                let offset = 0;
                for (const chunk of chunks) {
                    result.set(chunk, offset);
                    offset += chunk.length;
                }
                console.log('[Metadata] inflateZlib: deflate-raw 解压成功, 结果长度:', totalLen);
                return result;
            } catch (e) {
                console.warn('[Metadata] DecompressionStream deflate-raw 解压失败:', e.message);
                // 尝试 'gzip' 格式
                try {
                    console.log('[Metadata] inflateZlib: 尝试 gzip 格式...');
                    const ds2 = new DecompressionStream('gzip');
                    const writer2 = ds2.writable.getWriter();
                    writer2.write(data);
                    writer2.close();
                    const reader2 = ds2.readable.getReader();
                    const chunks2 = [];
                    while (true) {
                        const { done, value } = await reader2.read();
                        if (done) break;
                        chunks2.push(value);
                    }
                    const totalLen2 = chunks2.reduce((sum, c) => sum + c.length, 0);
                    const result2 = new Uint8Array(totalLen2);
                    let offset2 = 0;
                    for (const chunk of chunks2) {
                        result2.set(chunk, offset2);
                        offset2 += chunk.length;
                    }
                    console.log('[Metadata] inflateZlib: gzip 解压成功, 结果长度:', totalLen2);
                    return result2;
                } catch (e2) {
                    console.warn('[Metadata] DecompressionStream gzip 解压也失败:', e2.message);
                }
            }
        } else {
            console.warn('[Metadata] DecompressionStream 不可用');
        }

        // 方法2: 使用全局 pako 库（如果已加载）
        if (typeof pako !== 'undefined' && pako.inflate) {
            try {
                const result = pako.inflate(data);
                console.log('[Metadata] inflateZlib: pako 解压成功, 结果长度:', result.length);
                return result;
            } catch (e) {
                console.warn('[Metadata] pako 解压失败:', e.message);
            }
        }

        // 方法3: 尝试使用全局 pako 的旧版 API
        if (typeof window !== 'undefined' && window.pako && window.pako.inflate) {
            try {
                const result = window.pako.inflate(data);
                console.log('[Metadata] inflateZlib: window.pako 解压成功, 结果长度:', result.length);
                return result;
            } catch (e) {
                console.warn('[Metadata] window.pako 解压失败:', e.message);
            }
        }

        console.warn('[Metadata] 无法解压 zlib 数据，浏览器不支持 DecompressionStream，且未加载 pako 库');
        return null;
    }

    // ==================== PNG 解析 ====================

    function parsePNG(buffer) {
        const dataView = new DataView(buffer);
        const textChunks = {};

        // 验证 PNG 签名
        const signature = [137, 80, 78, 71, 13, 10, 26, 10];
        for (let i = 0; i < 8; i++) {
            if (dataView.getUint8(i) !== signature[i]) {
                return createEmptyResult();
            }
        }

        let offset = 8;
        while (offset < buffer.byteLength) {
            const length = dataView.getUint32(offset);
            const type = String.fromCharCode(
                dataView.getUint8(offset + 4),
                dataView.getUint8(offset + 5),
                dataView.getUint8(offset + 6),
                dataView.getUint8(offset + 7)
            );

            if (type === 'tEXt' || type === 'iTXt' || type === 'zTXt') {
                const chunkData = new Uint8Array(buffer, offset + 8, length);
                const text = decodePNGText(chunkData, type);
                const nullIdx = text.indexOf('\0');
                if (nullIdx > 0) {
                    const key = text.substring(0, nullIdx);
                    let value = text.substring(nullIdx + 1);

                    // iTXt 格式: Key\0<compression_flag><language_tag>\0<translated_keyword>\0<text>
                    // compression_flag: 1字节 (0=未压缩, 1=zlib压缩)
                    // language_tag: 以 \0 结尾的字符串（可能为空）
                    // translated_keyword: 以 \0 结尾的字符串（可能为空）
                    // 之后才是真正的文本内容
                    // 注意：SD WebUI Forge 可能在 translated_keyword 后多一个 \0
                    if (type === 'iTXt') {
                        if (value.length > 0) {
                            // compression_flag (1字节)
                            const compressionFlag = value.charCodeAt(0);
                            value = value.substring(1);
                            // language_tag: 跳过到下一个 \0
                            const langEnd = value.indexOf('\0');
                            if (langEnd >= 0) {
                                value = value.substring(langEnd + 1);
                                // translated_keyword: 跳过到下一个 \0
                                const kwEnd = value.indexOf('\0');
                                if (kwEnd >= 0) {
                                    value = value.substring(kwEnd + 1);
                                    // 跳过可能存在的额外 \0（SD WebUI Forge 特性）
                                    while (value.length > 0 && value.charCodeAt(0) === 0) {
                                        value = value.substring(1);
                                    }
                                }
                            }
                            // 如果是压缩的，需要解压
                            if (compressionFlag === 1) {
                                // value 此时是字符串形式的二进制数据，需要转回 Uint8Array
                                // 但我们已经用 TextDecoder 解码了，所以需要从原始 chunkData 中提取压缩数据
                                // 重新从原始数据中提取压缩部分
                                const rawBytes = new Uint8Array(chunkData);
                                // 找到 key 的末尾（第一个 \0）
                                let keyEnd = 0;
                                while (keyEnd < rawBytes.length && rawBytes[keyEnd] !== 0) keyEnd++;
                                if (keyEnd < rawBytes.length) {
                                    // 跳过 key\0
                                    let dataStart = keyEnd + 1;
                                    // 跳过 compression_flag (1字节)
                                    dataStart++;
                                    // 跳过 language_tag (到下一个 \0)
                                    while (dataStart < rawBytes.length && rawBytes[dataStart] !== 0) dataStart++;
                                    dataStart++; // 跳过 \0
                                    // 跳过 translated_keyword (到下一个 \0)
                                    while (dataStart < rawBytes.length && rawBytes[dataStart] !== 0) dataStart++;
                                    dataStart++; // 跳过 \0
                                    // 跳过可能存在的额外 \0
                                    while (dataStart < rawBytes.length && rawBytes[dataStart] === 0) dataStart++;
                                    
                                    // 剩余部分就是压缩数据
                                    const compressedData = rawBytes.slice(dataStart);
                                    if (compressedData.length > 0) {
                                        // 异步解压，但 parsePNG 是同步的，所以先标记为需要解压
                                        // 在 universalParse 中处理异步解压
                                        textChunks[key] = { _compressed: true, _data: compressedData, _rawValue: value };
                                        continue; // 跳过下面的 textChunks[key] = value
                                    }
                                }
                                value = '[compressed]';
                            }
                        }
                    }

                    textChunks[key] = value;
                }
            }

            if (type === 'IEND') break;
            offset += 12 + length;
        }

        return universalParse(textChunks);
    }

    function decodePNGText(data, type) {
        if (type === 'zTXt') {
            try {
                const decoder = new TextDecoder();
                let str = decoder.decode(data);
                const nullIdx = str.indexOf('\0');
                if (nullIdx > 0) {
                    const key = str.substring(0, nullIdx);
                    // zTXt 压缩数据从 key\0 之后开始（跳过 compression_method 1字节）
                    const rawBytes = new Uint8Array(data);
                    let dataStart = nullIdx + 1;
                    // compression_method: 1字节 (0=zlib压缩)
                    if (dataStart < rawBytes.length) {
                        dataStart++; // 跳过 compression_method
                        const compressedData = rawBytes.slice(dataStart);
                        if (compressedData.length > 0) {
                            // 返回特殊标记，让 parsePNG 处理解压
                            return key + '\0' + JSON.stringify({ _zTXt_compressed: true, _data: Array.from(compressedData) });
                        }
                    }
                    return str;
                }
                return str;
            } catch (e) {
                return new TextDecoder().decode(data);
            }
        }
        return new TextDecoder().decode(data);
    }

    // ==================== JPEG 解析 ====================

    function parseJPEG(buffer) {
        const dataView = new DataView(buffer);
        const textChunks = {};

        if (dataView.getUint16(0) !== 0xFFD8) {
            return createEmptyResult();
        }

        let offset = 2;
        while (offset < buffer.byteLength - 1) {
            const marker = dataView.getUint16(offset);

            if (marker === 0xFFE1) {
                // EXIF data in APP1
                const length = dataView.getUint16(offset + 2);
                const exifData = new Uint8Array(buffer, offset + 4, length - 2);
                const exifText = extractEXIFText(exifData);
                if (exifText) {
                    textChunks['exif'] = exifText;
                }
                offset += 2 + length;
            } else if (marker === 0xFFFE) {
                // COM segment
                const length = dataView.getUint16(offset + 2);
                const comData = new Uint8Array(buffer, offset + 4, length - 2);
                textChunks['Comment'] = new TextDecoder().decode(comData);
                offset += 2 + length;
            } else if (marker === 0xFFED) {
                // Photoshop IRB (APP13)
                const length = dataView.getUint16(offset + 2);
                offset += 2 + length;
            } else if ((marker >= 0xFFE0 && marker <= 0xFFEF) ||
                       marker === 0xFFDB || marker === 0xFFC4 ||
                       marker === 0xFFC0 || marker === 0xFFC2) {
                const length = dataView.getUint16(offset + 2);
                offset += 2 + length;
            } else if (marker === 0xFFDA) {
                // SOS - start of scan
                break;
            } else {
                offset += 2;
            }
        }

        if (Object.keys(textChunks).length === 0) {
            return createEmptyResult();
        }

        return universalParse(textChunks);
    }

    /**
     * 从 EXIF 数据中提取文本内容
     * 尝试多种方式：JSON、参数字符串、纯文本
     */
    function extractEXIFText(exifData) {
        try {
            const decoder = new TextDecoder();
            const str = decoder.decode(exifData);

            // 尝试找 JSON
            const jsonMatch = str.match(/\{[\s\S]*\}/);
            if (jsonMatch) {
                try {
                    JSON.parse(jsonMatch[0]);
                    return jsonMatch[0];
                } catch (e) {
                    // 不是有效 JSON，继续尝试
                }
            }

            // 尝试找 "parameters" 模式（A1111 风格）
            const paramMatch = str.match(/parameters[\0\s]*([\s\S]*?)(?:\0|$)/);
            if (paramMatch && paramMatch[1].trim()) {
                return paramMatch[1].trim();
            }

            // 返回清理后的文本
            const cleaned = str.replace(/[\x00-\x08\x0B\x0C\x0E-\x1F]/g, ' ').trim();
            return cleaned || null;
        } catch (e) {
            return null;
        }
    }

    // ==================== WebP 解析 ====================

    function parseWebP(buffer) {
        const dataView = new DataView(buffer);
        const textChunks = {};

        if (dataView.getUint32(0) !== 0x52494646) return createEmptyResult(); // 'RIFF'
        if (dataView.getUint32(8) !== 0x57454250) return createEmptyResult(); // 'WEBP'

        let offset = 12;
        while (offset < buffer.byteLength - 8) {
            const chunkType = String.fromCharCode(
                dataView.getUint8(offset),
                dataView.getUint8(offset + 1),
                dataView.getUint8(offset + 2),
                dataView.getUint8(offset + 3)
            );
            const chunkSize = dataView.getUint32(offset + 4, true);

            if (chunkType === 'EXIF' || chunkType === 'XMP ') {
                const chunkData = new Uint8Array(buffer, offset + 8, chunkSize);
                const text = new TextDecoder().decode(chunkData);
                textChunks[chunkType] = text;
            }

            offset += 8 + chunkSize;
            if (chunkSize % 2 !== 0) offset += 1;
        }

        if (Object.keys(textChunks).length === 0) return createEmptyResult();
        return universalParse(textChunks);
    }

    // ==================== 通用语义解析引擎 ====================

    /**
     * 通用解析入口
     * 接收所有文本块，通过语义分析提取 prompt 和参数
     * @param {Object} textChunks - { key: value } 格式的文本块
     * @returns {Object} 标准化结果
     */
    function universalParse(textChunks) {
        const result = createEmptyResult();
        result.raw = { ...textChunks };

        // 处理压缩数据：将 _compressed 标记的数据解压
        for (const [key, value] of Object.entries(textChunks)) {
            if (value && typeof value === 'object' && value._compressed) {
                // 异步解压，但这里需要同步返回
                // 使用 Promise 在后台解压，但当前返回标记
                // 实际使用时，parseFile/parseBuffer 会处理异步
                inflateZlib(value._data).then(decompressed => {
                    if (decompressed) {
                        const decoded = new TextDecoder().decode(decompressed);
                        textChunks[key] = decoded;
                        result.raw[key] = decoded;
                    }
                }).catch(err => {
                    console.warn('[Metadata] 解压失败:', key, err);
                });
                // 暂时保留原始值
                textChunks[key] = value._rawValue || '[compressed]';
            }
            // 处理 zTXt 压缩数据
            if (typeof value === 'string' && value.startsWith('{"_zTXt_compressed":true')) {
                try {
                    const parsed = JSON.parse(value);
                    if (parsed._zTXt_compressed) {
                        const compressedData = new Uint8Array(parsed._data);
                        inflateZlib(compressedData).then(decompressed => {
                            if (decompressed) {
                                const decoded = new TextDecoder().decode(decompressed);
                                textChunks[key] = decoded;
                                result.raw[key] = decoded;
                            }
                        }).catch(err => {
                            console.warn('[Metadata] zTXt 解压失败:', key, err);
                        });
                        textChunks[key] = '[compressed]';
                    }
                } catch (e) {
                    // 不是有效的 JSON 标记，保持原样
                }
            }
        }

        // 第一步：将所有文本块的值收集到数据池
        const dataPool = buildDataPool(textChunks);

        // 第二步：从数据池中提取 prompt（正向/负向）
        extractPrompts(dataPool, result);

        // 第三步：从数据池中提取所有参数
        extractAllParams(dataPool, result);

        // 第四步：后处理 - 合并 Size 参数
        postProcessParams(result);

        return result;
    }

    /**
     * 构建数据池
     * 将各种格式的原始数据统一为可遍历的数据项列表
     * 每个数据项: { source: 'key'|'json'|'text', key: string, value: any, raw: string }
     */
    function buildDataPool(textChunks) {
        const pool = [];

        for (const [key, value] of Object.entries(textChunks)) {
            if (!value || typeof value !== 'string') continue;

            const trimmedValue = value.trim();
            if (!trimmedValue) continue;

            // 尝试解析为 JSON
            let jsonData = null;
            try {
                jsonData = JSON.parse(trimmedValue);
            } catch (e) {
                // 不是 JSON，保持为文本
            }

            if (jsonData && typeof jsonData === 'object') {
                // JSON 数据：深度遍历提取所有字段
                pool.push({
                    source: key,
                    type: 'json',
                    key: key,
                    value: jsonData,
                    raw: trimmedValue
                });
                // 递归提取 JSON 中的所有叶子节点
                flattenJSON(jsonData, key, '', pool);
                
                // 特殊处理 SwarmUI 的 sui_image_params 格式
                // { "sui_image_params": { "prompt": "...", "negative_prompt": "...", "steps": 20, ... } }
                if (jsonData.sui_image_params && typeof jsonData.sui_image_params === 'object') {
                    const params = jsonData.sui_image_params;
                    // 提取 prompt
                    if (params.prompt && typeof params.prompt === 'string') {
                        pool.push({
                            source: key,
                            type: 'text',
                            key: 'prompt',
                            value: params.prompt,
                            raw: params.prompt
                        });
                    }
                    // 提取 negative_prompt
                    if (params.negative_prompt && typeof params.negative_prompt === 'string') {
                        pool.push({
                            source: key,
                            type: 'text',
                            key: 'negative_prompt',
                            value: params.negative_prompt,
                            raw: params.negative_prompt
                        });
                    }
                }
                
                // 特殊处理 ComfyUI 的 workflow 格式
                // workflow 中包含 nodes 数组，每个 node 有 widgets_values
                if (key === 'workflow' && jsonData.nodes && Array.isArray(jsonData.nodes)) {
                    extractComfyUIParams(jsonData.nodes, key, pool);
                }
            } else {
                // 纯文本数据
                pool.push({
                    source: key,
                    type: 'text',
                    key: key,
                    value: trimmedValue,
                    raw: trimmedValue
                });
            }
        }

        return pool;
    }

    /**
     * 从 ComfyUI workflow 的 nodes 中提取生成参数
     * ComfyUI 的 workflow JSON 包含 nodes 数组，每个 node 有 type 和 widgets_values
     * 例如：KSampler node 的 widgets_values 包含 [seed, steps, cfg, sampler_name, scheduler, denoise]
     */
    function extractComfyUIParams(nodes, source, pool) {
        // ComfyUI 节点类型到参数的映射
        // widgets_values 的索引因节点版本而异，这里使用通用索引
        // 同时优先使用 widget_idx_map（如果存在）来获取准确映射
        const NODE_PARAM_MAP = {
            'KSampler': { seed: 0, steps: 2, cfg: 3, sampler_name: 4, scheduler: 5, denoise: 6 },
            'KSamplerAdvanced': { seed: 0, steps: 2, cfg: 3, sampler_name: 4, scheduler: 5, denoise: 6 },
            'VAEDecode': {},
            'CLIPTextEncode': { text: 0 },
            'CLIPTextEncode (NSP)': { text: 0 },
            'CheckpointLoaderSimple': { ckpt_name: 0 },
            'UNETLoader': { unet_name: 0 },
            'UnetLoaderGGUF': { unet_name: 0 },
            'CLIPLoader': { clip_name: 0 },
            'VAELoader': { vae_name: 0 },
            'LoraLoader': { lora_name: 0, strength: 1 },
            'LoraLoaderModelOnly': { lora_name: 0, strength: 1 },
            'ControlNetLoader': { control_net_name: 0 },
            'UpscaleModelLoader': { model_name: 0 },
            'ImageUpscaleWithModel': {},
            'EmptyLatentImage': { width: 0, height: 1, batch_size: 2 },
            'ModelSamplingSD3': { shift: 0 },
        };

        // 收集所有节点的 widget_idx_map（如果有）
        const widgetIdxMap = {};
        if (nodes._meta && nodes._meta.widget_idx_map) {
            Object.assign(widgetIdxMap, nodes._meta.widget_idx_map);
        }
        // 有些 workflow 把 widget_idx_map 放在顶层
        if (nodes.widget_idx_map) {
            Object.assign(widgetIdxMap, nodes.widget_idx_map);
        }

        for (const node of nodes) {
            if (!node || !node.type) continue;
            if (node.id === undefined) continue;
            const nodeType = node.type;
            const nodeId = String(node.id);
            
            // 优先使用 widget_idx_map 中的映射
            const nodeWidgetMap = widgetIdxMap[nodeId];
            
            const widgets = node.widgets_values;
            if (!widgets || !Array.isArray(widgets)) continue;

            // 如果有 widget_idx_map，用它来提取参数
            if (nodeWidgetMap && typeof nodeWidgetMap === 'object') {
                for (const [paramName, index] of Object.entries(nodeWidgetMap)) {
                    if (index < widgets.length && widgets[index] !== null && widgets[index] !== undefined) {
                        const val = widgets[index];
                        // 跳过 "randomize" 等控制值
                        if (typeof val === 'string' && /^(randomize|fixed|increment|decrement)$/i.test(val)) continue;
                        pool.push({
                            source: source,
                            type: 'json_leaf',
                            key: 'comfy.' + paramName,
                            value: String(val),
                            raw: String(val)
                        });
                    }
                }
            } else {
                // 没有 widget_idx_map，使用通用映射
                const mapping = NODE_PARAM_MAP[nodeType];
                if (!mapping) continue;

                for (const [paramName, index] of Object.entries(mapping)) {
                    if (index < widgets.length && widgets[index] !== null && widgets[index] !== undefined) {
                        const val = widgets[index];
                        // 跳过 "randomize" 等控制值
                        if (typeof val === 'string' && /^(randomize|fixed|increment|decrement)$/i.test(val)) continue;
                        pool.push({
                            source: source,
                            type: 'json_leaf',
                            key: 'comfy.' + paramName,
                            value: String(val),
                            raw: String(val)
                        });
                    }
                }
            }

            // 从 inputs 中提取参数（ComfyUI 新版格式）
            if (node.inputs && typeof node.inputs === 'object') {
                const allowedInputNames = new Set([
                    'seed', 'steps', 'cfg', 'sampler_name', 'scheduler', 'denoise',
                    'width', 'height', 'text', 'ckpt_name', 'model', 'vae',
                    'clip_name', 'lora_name', 'strength', 'batch_size', 'guidance',
                    'unet_name', 'shift'
                ]);
                for (const [inputName, inputVal] of Object.entries(node.inputs)) {
                    if (!allowedInputNames.has(inputName)) continue;
                    if (inputVal !== null && inputVal !== undefined && typeof inputVal !== 'object') {
                        pool.push({
                            source: source,
                            type: 'json_leaf',
                            key: 'comfy.' + nodeType + '.' + inputName,
                            value: String(inputVal),
                            raw: String(inputVal)
                        });
                    }
                }
            }
        }
    }

    /**
     * 递归展平 JSON 对象，将所有叶子节点加入数据池
     * 不依赖节点类型名称，而是提取所有有意义的键值对
     */
    function flattenJSON(obj, source, path, pool, depth = 0) {
        if (depth > 20) return; // 防止无限递归
        if (!obj || typeof obj !== 'object') return;

        if (Array.isArray(obj)) {
            // 数组：检查是否是 ComfyUI 风格的连接引用 [nodeId, slotIndex]
            // 如果是短数组且元素是数字/字符串，可能是连接引用，跳过
            if (obj.length <= 3 && obj.every(v => typeof v === 'number' || (typeof v === 'string' && v.length < 10))) {
                return; // 跳过连接引用
            }
            // 遍历数组元素
            for (let i = 0; i < obj.length; i++) {
                if (obj[i] && typeof obj[i] === 'object') {
                    flattenJSON(obj[i], source, path + '[' + i + ']', pool, depth + 1);
                } else if (typeof obj[i] === 'string' && obj[i].length > 0) {
                    pool.push({
                        source: source,
                        type: 'json_leaf',
                        key: path + '[' + i + ']',
                        value: obj[i],
                        raw: String(obj[i])
                    });
                }
            }
            return;
        }

        // 对象：遍历所有键
        for (const [key, val] of Object.entries(obj)) {
            const currentPath = path ? path + '.' + key : key;

            if (val === null || val === undefined) continue;

            if (typeof val === 'object' && !Array.isArray(val)) {
                // 嵌套对象：继续递归
                flattenJSON(val, source, currentPath, pool, depth + 1);
            } else if (Array.isArray(val)) {
                // 数组值
                if (val.length <= 3 && val.every(v => typeof v === 'number' || (typeof v === 'string' && v.length < 10))) {
                    // 可能是连接引用，但如果是简单值数组（如尺寸 [512, 768]），则记录
                    if (val.length === 2 && val.every(v => typeof v === 'number')) {
                        pool.push({
                            source: source,
                            type: 'json_leaf',
                            key: currentPath,
                            value: val,
                            raw: JSON.stringify(val)
                        });
                    }
                    continue;
                }
                // 复杂数组，递归
                for (let i = 0; i < val.length; i++) {
                    if (val[i] && typeof val[i] === 'object') {
                        flattenJSON(val[i], source, currentPath + '[' + i + ']', pool, depth + 1);
                    }
                }
            } else {
                // 叶子节点：字符串、数字、布尔值
                if (!shouldSkipJsonLeaf(currentPath, val, source)) {
                    pool.push({
                        source: source,
                        type: 'json_leaf',
                        key: currentPath,
                        value: val,
                        raw: String(val)
                    });
                }
            }
        }
    }

    /**
     * 从数据池中提取正向和负向提示词
     * 
     * 策略：
     * 1. 先找明确标记为 "prompt" / "positive" 的字段
     * 2. 再找明确标记为 "negative" 的字段
     * 3. 对于 A1111 格式的参数字符串，按 "Negative prompt:" 分割
     * 4. 如果都没找到，取最长的自然语言文本作为 prompt
     */
    function extractPrompts(dataPool, result) {
        let positiveCandidates = [];
        let negativeCandidates = [];
        let allTextItems = [];

        // 第一轮：按字段名分类
        for (const item of dataPool) {
            const keyLower = item.key.toLowerCase();
            
            // 跳过非字符串值（对象、数组等不能作为 prompt）
            if (typeof item.value !== 'string') continue;
            
            const valStr = item.value;

            // 跳过太短的值
            if (valStr.length < 3) continue;

            // 检查是否是明确的 prompt 字段
            if (isPromptKey(keyLower)) {
                positiveCandidates.push({ text: valStr, priority: getKeyPriority(keyLower, 'positive') });
            } else if (isNegativeKey(keyLower)) {
                negativeCandidates.push({ text: valStr, priority: getKeyPriority(keyLower, 'negative') });
            } else if (isParamKey(keyLower)) {
                // 参数键，不参与 prompt 判断
                continue;
            } else if (item.type === 'text' && valStr.length > 20) {
                // 长文本，可能是 prompt
                allTextItems.push({ text: valStr, key: item.key });
            } else if (item.type === 'json_leaf' && valStr.length > 20) {
                allTextItems.push({ text: valStr, key: item.key });
            }
        }

        // 处理 A1111 格式：参数字符串中包含 "Negative prompt:"
        for (const item of allTextItems) {
            if (item.text.includes('\nNegative prompt:')) {
                const negIdx = item.text.indexOf('\nNegative prompt:');
                const posPart = item.text.substring(0, negIdx).trim();
                const negPart = item.text.substring(negIdx + '\nNegative prompt:'.length).trim();

                // 清理 negPart 中可能跟随的参数行
                const negCleanMatch = negPart.match(/^([\s\S]*?)(?:\n[A-Z][a-z]+(?:\s[A-Z])?:|\n\s*$|$)/);
                const negClean = negCleanMatch ? negCleanMatch[1].trim() : negPart;

                if (posPart.length > 10) {
                    positiveCandidates.push({ text: posPart, priority: 10 });
                }
                if (negClean.length > 1) {
                    negativeCandidates.push({ text: negClean, priority: 10 });
                }
                // 从 allTextItems 中移除已处理的项
                item.processed = true;
            }
        }

        // 按优先级排序
        positiveCandidates.sort((a, b) => b.priority - a.priority);
        negativeCandidates.sort((a, b) => b.priority - a.priority);

        // 选择最佳候选
        if (positiveCandidates.length > 0) {
            result.prompt = positiveCandidates[0].text;
        } else {
            // 从剩余文本中找最长的自然语言文本
            const remaining = allTextItems.filter(t => !t.processed && t.text.length > 30);
            remaining.sort((a, b) => b.text.length - a.text.length);
            if (remaining.length > 0) {
                // 检查是否像自然语言（包含空格和常见单词）
                let bestText = remaining.find(t => looksLikeNaturalLanguage(t.text));
                
                // 如果整段文本不是自然语言（可能混合了 prompt + 参数），
                // 尝试提取第一行（SD WebUI 格式: prompt 在第一行，参数在第二行）
                if (!bestText) {
                    for (const item of remaining) {
                        const firstLine = item.text.split('\n')[0].trim();
                        if (firstLine.length > 20 && looksLikeNaturalLanguage(firstLine)) {
                            bestText = { text: firstLine };
                            break;
                        }
                    }
                }
                
                if (bestText) {
                    result.prompt = bestText.text;
                }
            }
        }

        if (negativeCandidates.length > 0) {
            result.negativePrompt = negativeCandidates[0].text;
        }
    }

    /**
     * 判断键名是否指向正向提示词
     */
    function isPromptKey(key) {
        const positivePatterns = [
            'prompt', 'positive', 'pos', 'text', 'caption',
            'description', 'input_text', 'positive_prompt', 'pos_prompt',
            'text_positive', 'text_p', 'prompt_pos'
        ];
        return positivePatterns.some(p => key === p || key.endsWith('.' + p) || key.includes('.prompt.'));
    }

    /**
     * 判断键名是否指向负向提示词
     */
    function isNegativeKey(key) {
        const negativePatterns = [
            'negative', 'neg', 'negative_prompt', 'neg_prompt',
            'uc', 'unconditioned', 'negativeprompt', 'text_negative',
            'text_n', 'prompt_neg', 'negative text', 'negative_text'
        ];
        return negativePatterns.some(p => key === p || key.endsWith('.' + p) || key.includes('.negative'));
    }

    /**
     * 判断键名是否是参数（而非 prompt）
     */
    function isParamKey(key) {
        const paramPatterns = [
            'steps', 'cfg', 'seed', 'sampler', 'scheduler',
            'width', 'height', 'size', 'model', 'vae', 'lora',
            'batch', 'denoise', 'upscale', 'hires', 'clip_skip',
            'ensd', 'refiner', 'control', 'checkpoint'
        ];
        return paramPatterns.some(p => key.includes(p));
    }

    /**
     * 获取键名的优先级（用于选择最佳 prompt 候选）
     */
    function getKeyPriority(key, type) {
        // 精确匹配优先级最高
        if (type === 'positive') {
            if (key === 'prompt' || key === 'positive_prompt' || key === 'pos_prompt') return 100;
            if (key === 'text' || key === 'caption') return 80;
            if (key.includes('prompt')) return 60;
            return 40;
        } else {
            if (key === 'negative_prompt' || key === 'neg_prompt' || key === 'uc') return 100;
            if (key === 'negative' || key === 'neg') return 80;
            if (key.includes('negative')) return 60;
            return 40;
        }
    }

    /**
     * 判断文本是否像自然语言
     */
    function looksLikeNaturalLanguage(text) {
        if (!text || text.length < 20) return false;
        // 包含空格（词间分隔）
        const hasSpaces = text.includes(' ');
        // 包含常见英文单词模式
        const hasWords = /[a-zA-Z]{3,}/.test(text);
        // 不是纯参数格式（Key: Value, Key: Value）
        const isNotParams = !/^[A-Z][a-z]+(\s[A-Z][a-z]+)?:\s*.+$/m.test(text);
        // 不是纯 JSON
        const isNotJSON = !/^\s*[\{\[]/.test(text);
        return hasSpaces && hasWords && isNotParams && isNotJSON;
    }

    /**
     * 从数据池中提取所有参数
     * 
     * 策略：
     * 1. 对文本类型数据，用 Key: Value 正则匹配
     * 2. 对 JSON 叶子节点，用键名匹配参数别名表
     * 3. 对特殊格式（如 A1111 参数行），按逗号分割解析
     */
    function extractAllParams(dataPool, result) {
        const rawParams = {}; // 原始参数收集 { originalKey: value }

        for (const item of dataPool) {
            if (item.type === 'text') {
                // 文本类型：尝试 Key: Value 模式匹配
                extractParamsFromText(item.value, rawParams);
            } else if (item.type === 'json_leaf') {
                // JSON 叶子节点：用键名匹配
                const keyName = extractLeafKeyName(item.key);
                if (keyName && !isPromptKey(keyName.toLowerCase()) && !isNegativeKey(keyName.toLowerCase())) {
                    const val = item.value;
                    if (val !== null && val !== undefined && val !== '') {
                        rawParams[keyName] = typeof val === 'object' ? JSON.stringify(val) : String(val);
                    }
                }
            }
        }

        // 标准化参数名
        for (const [rawKey, rawValue] of Object.entries(rawParams)) {
            const standardKey = normalizeParamName(rawKey);
            if (standardKey) {
                // 如果标准化后的键名在非参数列表中，跳过
                const isKnownParam = PARAM_ALIASES[standardKey] !== undefined;
                const rawKeyLower = rawKey.toLowerCase();
                
                // 只对未识别的键进行非参数过滤
                if (!isKnownParam && NON_PARAM_KEYS.has(rawKeyLower)) {
                    continue;
                }
                
                const strValue = String(rawValue);

                if (shouldSkipNormalizedParam(rawKey, rawValue, standardKey)) {
                    continue;
                }
                
                // 如果已存在，智能选择更合适的值
                if (result.params[standardKey]) {
                    // 对于 Model 参数，优先选择包含模型文件扩展名的值
                    if (standardKey === 'Model') {
                        const existing = result.params[standardKey];
                        const newIsModelFile = /\.(safetensors|ckpt|pt|pth|onnx|bin)$/i.test(strValue);
                        const oldIsModelFile = /\.(safetensors|ckpt|pt|pth|onnx|bin)$/i.test(existing);
                        const newIsHash = /^[a-f0-9]{6,}$/i.test(strValue);
                        const oldIsHash = /^[a-f0-9]{6,}$/i.test(existing);
                        const newIsControlNetModel = /sai_xl_canny|control|cn_|t2i-adapter/i.test(strValue);
                        const oldIsControlNetModel = /sai_xl_canny|control|cn_|t2i-adapter/i.test(existing);
                        
                        // 如果新值是 ControlNet 模型，保留旧值（主模型）
                        if (newIsControlNetModel && !oldIsControlNetModel) {
                            // 跳过，保留主模型
                            continue;
                        }
                        // 如果旧值是 ControlNet 模型，用新值替换（新值可能是主模型）
                        else if (!newIsControlNetModel && oldIsControlNetModel) {
                            result.params[standardKey] = strValue;
                        }
                        // 如果新值是模型文件名且旧值是哈希，替换
                        else if (newIsModelFile && oldIsHash) {
                            result.params[standardKey] = strValue;
                        }
                        // 如果新值不是哈希且旧值是哈希，替换
                        else if (!newIsHash && oldIsHash && strValue.length > 3) {
                            result.params[standardKey] = strValue;
                        }
                    }
                    // 对于 LoRA 参数，合并多个值（名称 + 哈希）
                    else if (standardKey === 'LoRA') {
                        const existing = result.params[standardKey];
                        // 检查新值是否包含哈希映射格式（如 "name: hash"）
                        const newIsHashFormat = /["']?[a-zA-Z0-9_\-\.\s]+["']?\s*:\s*[a-f0-9]{6,}/i.test(strValue);
                        const existingIsHashFormat = /["']?[a-zA-Z0-9_\-\.\s]+["']?\s*:\s*[a-f0-9]{6,}/i.test(existing);
                        
                        if (newIsHashFormat && !existingIsHashFormat) {
                            // 新值是哈希映射，旧值是名称 → 合并：名称 + 哈希
                            result.params[standardKey] = existing + ' | ' + strValue;
                        } else if (!newIsHashFormat && existingIsHashFormat) {
                            // 新值是名称，旧值是哈希映射 → 合并：名称 + 哈希
                            result.params[standardKey] = strValue + ' | ' + existing;
                        } else if (!newIsHashFormat && !existingIsHashFormat && existing !== strValue) {
                            // 两者都是名称但不同 → 合并（使用 | 分隔，与 detail.js 解析保持一致）
                            result.params[standardKey] = existing + ' | ' + strValue;
                        }
                        // 如果两者都是哈希格式，保留第一个（通常更完整）
                    }
                    // 对于其他参数，保留第一个非空值
                } else {
                    result.params[standardKey] = strValue;
                }
            }
        }
    }

    /**
     * 从文本中提取 Key: Value 格式的参数
     * 支持多种分隔符：冒号、等号、逗号分隔的多参数行
     */
    function extractParamsFromText(text, rawParams) {
        if (!text || typeof text !== 'string') return;

        // 先按行分割
        const lines = text.split(/\n\r?/);

        for (const line of lines) {
            const trimmed = line.trim();
            if (!trimmed) continue;

            // 跳过明显的 prompt 文本行（太长的自然语言）
            if (trimmed.length > 300 && looksLikeNaturalLanguage(trimmed)) continue;

            // 跳过 "Negative prompt:" 行
            if (/^Negative\s*prompt\s*:/i.test(trimmed)) continue;

            // 跳过纯自然语言行（无 Key: Value 模式，但包含常见英文单词）
            // 用于处理 SD WebUI 中 prompt 和参数在同一行但无 Negative prompt 分隔的情况
            if (looksLikeNaturalLanguage(trimmed) && !/:\s/.test(trimmed)) continue;

            // 尝试按逗号分割多参数行（A1111 风格: "Steps: 20, Sampler: Euler a, CFG scale: 7"）
            // 使用更智能的分割：在 ", " 后跟大写字母的地方分割
            const segments = smartSplitParams(trimmed);

            for (const segment of segments) {
                // 跳过包含 <lora: 或 <lyco: 等标签的段（这些是 prompt 的一部分）
                if (/<lora:|<lyco:|<locon:|<embedding:/i.test(segment)) continue;

                // 尝试多种分隔符: "Key: Value" 或 "Key = Value"
                let match = segment.match(/^([A-Za-z][A-Za-z0-9_\s\-\.]*?)\s*[:=]\s*(.+)$/);
                if (match) {
                    const key = match[1].trim();
                    const value = match[2].trim();

                    // 过滤掉明显不是参数的内容
                    if (key.length > 50) continue; // 键���太长，可能是 prompt 文本
                    if (value.length > 200) continue; // 值太长，可能是 prompt 文本
                    // NON_PARAM_KEYS 检查移到标准化阶段，以便智能判断

                    rawParams[key] = value;
                }
            }
        }
    }

    /**
     * 智能分割参数行
     * "Steps: 20, Sampler: Euler a, CFG scale: 7, Seed: 12345"
     * -> ["Steps: 20", "Sampler: Euler a", "CFG scale: 7", "Seed: 12345"]
     */
    function smartSplitParams(line) {
        // 如果包含 ", " 且后面跟着参数模式（字母+空格+冒号），则分割
        // 支持: "Steps: 20", "CFG scale: 7", "Clip skip: 2", "Model hash: abc"
        // 注意：引号内的逗号不分割（如 ControlNet 配置 "Module: canny, Model: ..."）
        const segments = [];
        let current = '';
        let i = 0;
        let inQuotes = false;
        let quoteChar = null;

        while (i < line.length) {
            const ch = line[i];
            
            // 跟���引号状态
            if ((ch === '"' || ch === "'") && (i === 0 || line[i-1] !== '\\')) {
                if (!inQuotes) {
                    inQuotes = true;
                    quoteChar = ch;
                } else if (ch === quoteChar) {
                    inQuotes = false;
                    quoteChar = null;
                }
            }

            // 只在引号外进行分割
            if (!inQuotes && ch === ',' && (line[i + 1] === ' ' || i === line.length - 1)) {
                // 向前看，检查后面是否是新参数的开始
                const rest = line.substring(i + 1).trimStart();
                // 更宽松的匹配：字母开头 + 可能的空格/字母 + 冒号
                // 匹配 "Steps:", "CFG scale:", "Clip skip:", "Model hash:", "Size:", "ENSD:"
                if (/^[A-Za-z][A-Za-z0-9_\s]*[A-Za-z0-9_]\s*:/.test(rest)) {
                    // 是参数分隔符
                    if (current.trim()) {
                        segments.push(current.trim());
                    }
                    current = '';
                    i += 1; // 跳过逗号
                    if (line[i] === ' ') i++; // 跳过空格
                    continue;
                }
            }
            current += ch;
            i++;
        }

        if (current.trim()) {
            segments.push(current.trim());
        }

        // 如果没有分割出多个段，返回原始行
        return segments.length > 1 ? segments : [line];
    }

    function shouldSkipJsonLeaf(path, value, source) {
        const leafKey = extractLeafKeyName(path);
        const leafLower = (leafKey || '').toLowerCase();
        const pathLower = String(path || '').toLowerCase();
        const strValue = typeof value === 'string' ? value.trim() : String(value);

        if (NON_PARAM_KEYS.has(leafLower)) return true;

        if (source === 'workflow') {
            const parts = pathLower.split(/[\.\[\]]+/).filter(Boolean);

            if (parts.some(part => COMFY_NOISE_PATH_PARTS.has(part))) {
                if (!/(seed|steps|cfg|sampler|scheduler|denoise|width|height|ckpt_name|model|vae|clip|lora|strength|text|batch_size|guidance)/i.test(pathLower)) {
                    return true;
                }
            }

            if (/^https?:\/\//i.test(strValue)) return true;
            if (/^#[0-9a-f]{3,8}$/i.test(strValue)) return true;
            if (/^(INT|FLOAT|STRING|MODEL|IMAGE|LATENT|CONDITIONING|CLIP|VAE|BOOLEAN)$/i.test(strValue)) return true;
            if (strValue.includes('\n') && strValue.length > 40 && !/(prompt|negative)/i.test(pathLower)) return true;
        }

        return false;
    }

    /**
     * 从 JSON 路径中提取叶子键名
     * "workflow.nodes[0].widgets_values[0]" -> "widgets_values"
     * "prompt.3.inputs.text" -> "text"
     */
    function extractLeafKeyName(path) {
        if (!path) return null;
        // 取最后一个点或括号后的部分
        const parts = path.split(/[\.\[\]]+/).filter(Boolean);
        if (parts.length === 0) return null;

        // 跳过数字索引
        let lastName = parts[parts.length - 1];
        // 如果最后一部分是数字，取倒数第二个
        if (/^\d+$/.test(lastName) && parts.length > 1) {
            lastName = parts[parts.length - 2];
        }

        return lastName;
    }

    /**
     * 标准化参数名
     * 将各种 AI 工具的参数名映射到统一的标准名称
     *
     * 匹配策略（按优先级）：
     * 1. 精确匹配（忽略大小写）
     * 2. 单词边界匹配：别名作为完整单词出现在键名中（如 "model" 匹配 "model_name" 但不匹配 "model_hash"）
     * 3. 包含匹配：别名作为子串出现在键名中（仅对长度 >= 5 的别名）
     *
     * 注意：避免将通用短词（如 "scale"、"model"）误匹配到不相关的键名
     */
    function normalizeParamName(rawName) {
        const lowerName = rawName.toLowerCase().trim();

        // 先过滤一批明显属于 ComfyUI/工作流 UI 的噪声字段
        if (/^(filename_prefix|device|on|shift|w_ratio|h_ratio|scale_percent|reset|swap|preset|custom_presets|weight_dtype|name|show strengths|vhs_|seed_widgets)$/i.test(rawName)) {
            return null;
        }

        // 遍历别名映射表
        for (const [standardName, aliases] of Object.entries(PARAM_ALIASES)) {
            for (const alias of aliases) {
                const lowerAlias = alias.toLowerCase();

                // 1. 精确匹配（最高优先级）
                if (lowerName === lowerAlias) {
                    return standardName;
                }

                // 2. 单词边界匹配：别名作为完整单词出现（前后是单词边界）
                // 例如 "model" 匹配 "model_name" 但不匹配 "model_hash"（因为 hash 不是单词边界）
                // 使用正则 \b 单词边界
                if (alias.length >= 3) {
                    const boundaryRegex = new RegExp('\\b' + escapeRegex(lowerAlias) + '\\b');
                    if (boundaryRegex.test(lowerName)) {
                        return standardName;
                    }
                }

                // 3. 包含匹配（仅对较长别名，减少误匹配）
                // 例如 "denoising_strength" 匹配 "denoising strength"
                if (alias.length >= 5 && lowerName.includes(lowerAlias)) {
                    return standardName;
                }
            }
        }

        // 没有匹配到标准名，但看起来像参数，保留原始名称
        if (rawName.length > 1 && rawName.length < 40) {
            // 格式化：首字母大写，其余保持
            return rawName.charAt(0).toUpperCase() + rawName.slice(1);
        }

        return null;
    }

    function shouldSkipNormalizedParam(rawKey, rawValue, standardKey) {
        const rawKeyLower = String(rawKey || '').toLowerCase();
        const strValue = String(rawValue || '').trim();

        if (/^(id|revision|last_node_id|last_link_id|order|mode|link|slot_index|shape|dir)$/i.test(rawKeyLower)) {
            return true;
        }

        if (/^(color|bgcolor|url|directory|cnr_id|ver|node name for s&r|font_size|offset)$/i.test(rawKeyLower)) {
            return true;
        }

        if (/^(INT|FLOAT|STRING|MODEL|IMAGE|LATENT|CONDITIONING|CLIP|VAE|BOOLEAN)$/i.test(strValue)) {
            return true;
        }

        if (standardKey === 'Size' && /^\[\d+(\.\d+)?,\s*\d+(\.\d+)?\]$/.test(strValue)) {
            return true;
        }

        return false;
    }

    /**
     * 转义正则表达式中的特殊字符
     */
    function escapeRegex(str) {
        return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    }

    /**
     * 后处理参数
     * - 合并 Width/Height 为 Size
     * - 清理无效值
     */
    function postProcessParams(result) {
        const params = result.params;

        // 合并 Width + Height -> Size
        if (params['Width'] && params['Height']) {
            const w = params['Width'];
            const h = params['Height'];
            if (!params['Size']) {
                params['Size'] = `${w}x${h}`;
            }
            // 保留单独的 Width/Height 以便精确使用
        }

        // 如果只有 Size 没有 Width/Height，尝试从 Size 解析
        if (params['Size'] && (!params['Width'] || !params['Height'])) {
            const sizeMatch = String(params['Size']).match(/(\d+)\s*[x×X,]\s*(\d+)/);
            if (sizeMatch) {
                if (!params['Width']) params['Width'] = sizeMatch[1];
                if (!params['Height']) params['Height'] = sizeMatch[2];
            }
        }

        // 清理 Seed 值（去除可能的浮点数）
        if (params['Seed']) {
            const seedVal = String(params['Seed']);
            const seedNum = parseFloat(seedVal);
            if (!isNaN(seedNum)) {
                params['Seed'] = String(Math.round(seedNum));
            }
        }

        // 清理 CFG Scale 值
        if (params['CFG Scale']) {
            const cfgVal = parseFloat(String(params['CFG Scale']));
            if (!isNaN(cfgVal)) {
                params['CFG Scale'] = String(Math.round(cfgVal * 10) / 10);
            }
        }
    }

    // ==================== 辅助函数 ====================

    function createEmptyResult() {
        return {
            prompt: '',
            negativePrompt: '',
            params: {},
            raw: {}
        };
    }

    /**
     * 从图片 URL 加载并解析元数据
     */
    async function parseFromUrl(url) {
        try {
            const response = await fetch(url);
            const buffer = await response.arrayBuffer();
            const ext = url.split('.').pop().split('?')[0].toLowerCase();
            return await parseBuffer(buffer, ext);
        } catch (e) {
            console.warn('[MetadataParser] 无法从 URL 解析元数据:', url, e);
            return createEmptyResult();
        }
    }

    /**
     * 异步解析 PNG 元数据（支持压缩数据解压）
     * 与 parsePNG 不同，此方法会等待压缩数据解压完成
     * @param {ArrayBuffer} buffer
     * @returns {Promise<Object>}
     */
    async function parsePNGAsync(buffer) {
        const dataView = new DataView(buffer);
        const textChunks = {};
        const decompressPromises = [];

        console.log('[Metadata] parsePNGAsync: 开始, buffer大小:', buffer.byteLength);

        // 验证 PNG 签名
        const signature = [137, 80, 78, 71, 13, 10, 26, 10];
        for (let i = 0; i < 8; i++) {
            if (dataView.getUint8(i) !== signature[i]) {
                console.warn('[Metadata] parsePNGAsync: PNG签名验证失败');
                return createEmptyResult();
            }
        }

        let offset = 8;
        let textChunkCount = 0;
        while (offset < buffer.byteLength) {
            const length = dataView.getUint32(offset);
            const type = String.fromCharCode(
                dataView.getUint8(offset + 4),
                dataView.getUint8(offset + 5),
                dataView.getUint8(offset + 6),
                dataView.getUint8(offset + 7)
            );

            if (type === 'tEXt' || type === 'iTXt' || type === 'zTXt') {
                textChunkCount++;
                console.log('[Metadata] parsePNGAsync: 文本块 #' + textChunkCount, type, '长度:', length);
                const chunkData = new Uint8Array(buffer, offset + 8, length);
                
                if (type === 'zTXt') {
                    // zTXt: 直接处理压缩
                    const nullIdx = findNullByte(chunkData);
                    if (nullIdx > 0) {
                        const key = new TextDecoder().decode(chunkData.slice(0, nullIdx));
                        // compression_method (1字节)
                        const compressedData = chunkData.slice(nullIdx + 2); // +1 for \0, +1 for compression_method
                        if (compressedData.length > 0) {
                            // 创建解压 Promise
                            const promise = inflateZlib(compressedData).then(decompressed => {
                                if (decompressed) {
                                    textChunks[key] = new TextDecoder().decode(decompressed);
                                } else {
                                    textChunks[key] = '[compressed]';
                                }
                            }).catch(() => {
                                textChunks[key] = '[compressed]';
                            });
                            decompressPromises.push(promise);
                        }
                    }
                } else if (type === 'iTXt') {
                    // iTXt: 解析头部，处理可能的压缩
                    const nullIdx = findNullByte(chunkData);
                    if (nullIdx > 0) {
                        const key = new TextDecoder().decode(chunkData.slice(0, nullIdx));
                        let pos = nullIdx + 1;
                        
                        if (pos < chunkData.length) {
                            const compressionFlag = chunkData[pos];
                            pos++; // 跳过 compression_flag
                            
                            // 跳过 language_tag (到 \0)
                            while (pos < chunkData.length && chunkData[pos] !== 0) pos++;
                            pos++; // 跳过 \0
                            
                            // 跳过 translated_keyword (到 \0)
                            while (pos < chunkData.length && chunkData[pos] !== 0) pos++;
                            pos++; // 跳过 \0
                            
                            // 跳过可能存在的额外 \0（SD WebUI Forge 特性）
                            while (pos < chunkData.length && chunkData[pos] === 0) pos++;
                            
                            if (compressionFlag === 1) {
                                // 压缩数据
                                const compressedData = chunkData.slice(pos);
                                if (compressedData.length > 0) {
                                    const promise = inflateZlib(compressedData).then(decompressed => {
                                        if (decompressed) {
                                            textChunks[key] = new TextDecoder().decode(decompressed);
                                        } else {
                                            textChunks[key] = '[compressed]';
                                        }
                                    }).catch(() => {
                                        textChunks[key] = '[compressed]';
                                    });
                                    decompressPromises.push(promise);
                                }
                            } else {
                                // 未压缩
                                const textData = chunkData.slice(pos);
                                textChunks[key] = new TextDecoder().decode(textData);
                            }
                        }
                    }
                } else {
                    // tEXt: 普通文本
                    const text = new TextDecoder().decode(chunkData);
                    const nIdx = text.indexOf('\0');
                    if (nIdx > 0) {
                        textChunks[text.substring(0, nIdx)] = text.substring(nIdx + 1);
                    }
                }
            }

            if (type === 'IEND') break;
            offset += 12 + length;
        }

        // 等待所有解压完成
        console.log('[Metadata] parsePNGAsync: 等待解压, 解压任务数:', decompressPromises.length, '文本块数:', Object.keys(textChunks).length);
        await Promise.all(decompressPromises);
        console.log('[Metadata] parsePNGAsync: 解压完成, 文本块 keys:', Object.keys(textChunks));
        for (const [k, v] of Object.entries(textChunks)) {
            const valStr = typeof v === 'string' ? v : JSON.stringify(v);
            console.log('[Metadata] parsePNGAsync:   chunk[' + k + '] = ' + valStr.substring(0, 100) + '...');
        }

        return universalParse(textChunks);
    }

    /**
     * 在 Uint8Array 中查找第一个 null 字节的位置
     */
    function findNullByte(data) {
        for (let i = 0; i < data.length; i++) {
            if (data[i] === 0) return i;
        }
        return -1;
    }

    /**
     * 异步解析入口（支持压缩数据解压）
     * 重写 parseBufferInternal 以支持异步
     */
    async function parseBufferAsync(buffer, ext) {
        if (ext === 'png') return parsePNGAsync(buffer);
        if (ext === 'jpg' || ext === 'jpeg') return parseJPEG(buffer);
        if (ext === 'webp') return parseWebP(buffer);
        return createEmptyResult();
    }

    // ==================== 公开 API ====================

    return {
        parseFile,
        parseBuffer,
        parseFromUrl,
        parseBufferAsync,
        createEmptyResult
    };
})();
