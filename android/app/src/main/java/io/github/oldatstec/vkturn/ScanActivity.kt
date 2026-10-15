@file:Suppress("DEPRECATION") // android.hardware.Camera: camera2 is a page of code for a preview, and no AndroidX here

package io.github.oldatstec.vkturn

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.hardware.Camera
import android.os.Bundle
import android.os.Handler
import android.os.HandlerThread
import android.view.Gravity
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.widget.FrameLayout
import android.widget.TextView
import android.widget.Toast
import com.google.zxing.BarcodeFormat
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.MultiFormatReader
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.ReaderException
import com.google.zxing.common.HybridBinarizer

/**
 * The panel's QR code through the back camera: frames go to ZXing on a worker
 * thread, one at a time; the first vkturn:// text is the result
 * (EXTRA_TEXT).
 */
class ScanActivity : Activity(), SurfaceHolder.Callback, Camera.PreviewCallback {
    companion object {
        const val EXTRA_TEXT = "text"
        private const val REQ_CAMERA = 1
    }

    private lateinit var surface: SurfaceView
    private var camera: Camera? = null
    private var previewW = 0
    private var previewH = 0
    private var hasSurface = false
    @Volatile private var busy = false
    @Volatile private var done = false
    @Volatile private var warned = false

    private val worker = HandlerThread("qr").apply { start() }
    private val decoder = Handler(worker.looper)
    private val reader = MultiFormatReader().apply {
        setHints(mapOf(
            DecodeHintType.POSSIBLE_FORMATS to listOf(BarcodeFormat.QR_CODE),
            DecodeHintType.TRY_HARDER to true,
        ))
    }
    private val refocus = Handler(android.os.Looper.getMainLooper())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        title = getString(R.string.scan_title)
        surface = SurfaceView(this)
        surface.holder.addCallback(this)
        val hint = TextView(this).apply {
            text = getString(R.string.scan_hint)
            setTextColor(Color.WHITE)
            setBackgroundColor(0x99000000.toInt())
            setPadding(32, 24, 32, 24)
            gravity = Gravity.CENTER
        }
        setContentView(FrameLayout(this).apply {
            setBackgroundColor(Color.BLACK)
            addView(surface, FrameLayout.LayoutParams(FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT, Gravity.CENTER))
            addView(hint, FrameLayout.LayoutParams(FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.WRAP_CONTENT, Gravity.BOTTOM))
        })
        if (checkSelfPermission(Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.CAMERA), REQ_CAMERA)
        }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        if (requestCode != REQ_CAMERA) return
        if (grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) {
            start()
        } else {
            Toast.makeText(this, R.string.scan_no_camera, Toast.LENGTH_LONG).show()
            finish()
        }
    }

    override fun onResume() {
        super.onResume()
        start()
    }

    override fun onPause() {
        stop()
        super.onPause()
    }

    override fun onDestroy() {
        worker.quitSafely()
        super.onDestroy()
    }

    override fun surfaceCreated(holder: SurfaceHolder) {
        hasSurface = true
        start()
    }

    override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) {}

    override fun surfaceDestroyed(holder: SurfaceHolder) {
        hasSurface = false
        stop()
    }

    private fun start() {
        if (camera != null || !hasSurface || done) return
        if (checkSelfPermission(Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED) return
        val id = (0 until Camera.getNumberOfCameras()).firstOrNull {
            Camera.CameraInfo().also { i -> Camera.getCameraInfo(it, i) }.facing == Camera.CameraInfo.CAMERA_FACING_BACK
        } ?: 0
        val cam = try {
            Camera.open(id)
        } catch (e: RuntimeException) {
            Toast.makeText(this, R.string.scan_no_camera, Toast.LENGTH_LONG).show()
            finish()
            return
        }
        val info = Camera.CameraInfo().also { Camera.getCameraInfo(id, it) }
        val p = cam.parameters
        // The largest preview up to 1080p: a dense QR code wants the pixels,
        // the decoder wants no more.
        val size = p.supportedPreviewSizes
            .filter { it.width <= 1920 && it.height <= 1080 }
            .maxByOrNull { it.width * it.height } ?: p.previewSize
        p.setPreviewSize(size.width, size.height)
        previewW = size.width
        previewH = size.height
        val continuous = Camera.Parameters.FOCUS_MODE_CONTINUOUS_PICTURE in p.supportedFocusModes
        when {
            continuous -> p.focusMode = Camera.Parameters.FOCUS_MODE_CONTINUOUS_PICTURE
            Camera.Parameters.FOCUS_MODE_AUTO in p.supportedFocusModes -> p.focusMode = Camera.Parameters.FOCUS_MODE_AUTO
        }
        cam.parameters = p
        // The activity is portrait-only: the sensor's angle is the turn.
        cam.setDisplayOrientation(info.orientation % 360)
        fitSurface()
        try {
            cam.setPreviewDisplay(surface.holder)
        } catch (e: java.io.IOException) {
            cam.release()
            return
        }
        cam.setPreviewCallback(this)
        cam.startPreview()
        camera = cam
        if (!continuous && p.focusMode == Camera.Parameters.FOCUS_MODE_AUTO) autoFocusLoop()
    }

    /** The preview at its own aspect ratio, as wide as the screen (portrait: the sensor's width is the height). */
    private fun fitSurface() {
        val parent = surface.parent as? FrameLayout ?: return
        val w = parent.width.takeIf { it > 0 } ?: resources.displayMetrics.widthPixels
        val lp = surface.layoutParams as FrameLayout.LayoutParams
        lp.width = w
        lp.height = w * previewW / previewH
        surface.layoutParams = lp
    }

    private fun autoFocusLoop() {
        val cam = camera ?: return
        try {
            cam.autoFocus { _, _ -> }
        } catch (e: RuntimeException) {
        }
        refocus.postDelayed({ autoFocusLoop() }, 2000)
    }

    private fun stop() {
        refocus.removeCallbacksAndMessages(null)
        camera?.let {
            it.setPreviewCallback(null)
            it.stopPreview()
            it.release()
        }
        camera = null
    }

    @Deprecated("the framework Camera's")
    override fun onPreviewFrame(data: ByteArray, cam: Camera) {
        if (busy || done) return
        busy = true
        val w = previewW
        val h = previewH
        decoder.post {
            try {
                val src = PlanarYUVLuminanceSource(data, w, h, 0, 0, w, h, false)
                val text = reader.decodeWithState(BinaryBitmap(HybridBinarizer(src))).text
                if (text.contains("vkturn://")) {
                    done = true
                    runOnUiThread {
                        setResult(RESULT_OK, Intent().putExtra(EXTRA_TEXT, text))
                        finish()
                    }
                } else if (!warned) {
                    warned = true
                    runOnUiThread { Toast.makeText(this, R.string.scan_not_ours, Toast.LENGTH_SHORT).show() }
                }
            } catch (e: ReaderException) {
                // no code in this frame
            } catch (e: RuntimeException) {
            } finally {
                reader.reset()
                busy = false
            }
        }
    }
}
