// Image rules for the composer. Kept free of DOM access at import time so the
// validation helpers can run in node tests.

export const MAX_IMAGE_BYTES = 10 * 1024 * 1024
export const MAX_IMAGES_PER_MESSAGE = 5
export const COMPRESS_THRESHOLD_BYTES = 1.5 * 1024 * 1024
export const MAX_LONG_EDGE = 2048
export const JPEG_QUALITY = 0.85

const SUPPORTED_TYPES = ['image/png', 'image/jpeg', 'image/webp']
const SUPPORTED_EXTENSIONS = /\.(png|jpe?g|webp)$/i

export const GIF_MESSAGE = 'GIF images are not supported. Take a screenshot and upload the PNG instead.'
export const HEIC_MESSAGE = 'HEIC images are not supported. Export the photo as JPEG and try again.'
export const TYPE_MESSAGE = 'Only PNG, JPEG and WebP images are supported.'

// imageRejectReason returns a user-facing reason or an empty string.
export function imageRejectReason(file) {
  if (!file) return 'Choose an image file.'
  const name = String(file.name || '').toLowerCase()
  const type = String(file.type || '').toLowerCase()
  if (type === 'image/gif' || name.endsWith('.gif')) return GIF_MESSAGE
  if (type === 'image/heic' || type === 'image/heif' || name.endsWith('.heic') || name.endsWith('.heif')) {
    return HEIC_MESSAGE
  }
  if (type && !SUPPORTED_TYPES.includes(type)) return TYPE_MESSAGE
  // Some browsers report an empty MIME type; fall back to the extension.
  if (!type && !SUPPORTED_EXTENSIONS.test(name)) return TYPE_MESSAGE
  return ''
}

// needsCompression keeps small files untouched and shrinks everything else.
export function needsCompression(file) {
  return Boolean(file) && file.size > COMPRESS_THRESHOLD_BYTES
}

// compressedName always ends in .jpg because the canvas re-encodes to JPEG.
export function compressedName(file) {
  const name = String(file?.name || 'image')
  const base = name.replace(/\.[^.]+$/, '') || 'image'
  return `${base}.jpg`
}

function loadBitmap(file) {
  if (typeof createImageBitmap === 'function') {
    return createImageBitmap(file)
  }
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file)
    const image = new Image()
    image.onload = () => {
      URL.revokeObjectURL(url)
      resolve(image)
    }
    image.onerror = () => {
      URL.revokeObjectURL(url)
      reject(new Error('The image could not be read.'))
    }
    image.src = url
  })
}

// prepareImage shrinks the long edge to 2048px and re-encodes as JPEG 0.85.
// Small files are returned untouched only when they are also within the long
// edge limit, so a high-resolution screenshot is never uploaded at full size.
export async function prepareImage(file) {
  if (!file) return file
  const bitmap = await loadBitmap(file)
  const width = bitmap.width || bitmap.naturalWidth || 0
  const height = bitmap.height || bitmap.naturalHeight || 0
  if (!width || !height) throw new Error('The image could not be read.')
  const scale = Math.min(1, MAX_LONG_EDGE / Math.max(width, height))
  if (scale === 1 && file.size <= COMPRESS_THRESHOLD_BYTES) {
    if (typeof bitmap.close === 'function') bitmap.close()
    return file
  }
  const canvas = document.createElement('canvas')
  canvas.width = Math.max(1, Math.round(width * scale))
  canvas.height = Math.max(1, Math.round(height * scale))
  const context = canvas.getContext('2d')
  if (!context) throw new Error('The image could not be processed.')
  context.drawImage(bitmap, 0, 0, canvas.width, canvas.height)
  if (typeof bitmap.close === 'function') bitmap.close()
  const blob = await new Promise((resolve) => canvas.toBlob(resolve, 'image/jpeg', JPEG_QUALITY))
  if (!blob) throw new Error('The image could not be processed.')
  return new File([blob], compressedName(file), { type: 'image/jpeg', lastModified: Date.now() })
}
