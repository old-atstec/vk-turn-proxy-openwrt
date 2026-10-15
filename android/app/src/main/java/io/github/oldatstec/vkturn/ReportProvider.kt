package io.github.oldatstec.vkturn

import android.content.ContentProvider
import android.content.ContentValues
import android.content.Context
import android.database.Cursor
import android.database.MatrixCursor
import android.net.Uri
import android.os.ParcelFileDescriptor
import android.provider.OpenableColumns
import java.io.File
import java.io.FileNotFoundException

/**
 * Hands a report file to the app the user sends it with (a messenger, mail):
 * content://<package>.reports/<name>, read-only, only to whom the share
 * intent granted it. AndroidX's FileProvider in a dozen lines — the app has
 * no AndroidX. Only files of cacheDir/share, by name: no path reaches it.
 */
class ReportProvider : ContentProvider() {
    companion object {
        fun authority(ctx: Context) = ctx.packageName + ".reports"
        fun dir(ctx: Context) = File(ctx.cacheDir, "share").apply { mkdirs() }
        fun uri(ctx: Context, f: File): Uri = Uri.parse("content://${authority(ctx)}/${f.name}")
    }

    private fun file(uri: Uri): File {
        val name = uri.lastPathSegment ?: throw FileNotFoundException()
        if (name.contains('/') || name.startsWith(".")) throw FileNotFoundException()
        val f = File(dir(context!!), name)
        if (!f.isFile) throw FileNotFoundException(name)
        return f
    }

    override fun onCreate() = true

    override fun getType(uri: Uri) = "text/plain"

    override fun openFile(uri: Uri, mode: String): ParcelFileDescriptor {
        if (mode != "r") throw SecurityException("read-only")
        return ParcelFileDescriptor.open(file(uri), ParcelFileDescriptor.MODE_READ_ONLY)
    }

    override fun query(uri: Uri, projection: Array<out String>?, selection: String?, args: Array<out String>?, sort: String?): Cursor {
        val f = file(uri)
        val cols = projection ?: arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE)
        return MatrixCursor(cols, 1).apply {
            addRow(cols.map { if (it == OpenableColumns.SIZE) f.length() else if (it == OpenableColumns.DISPLAY_NAME) f.name else null })
        }
    }

    override fun insert(uri: Uri, values: ContentValues?): Uri? = null
    override fun delete(uri: Uri, selection: String?, args: Array<out String>?) = 0
    override fun update(uri: Uri, values: ContentValues?, selection: String?, args: Array<out String>?) = 0
}
