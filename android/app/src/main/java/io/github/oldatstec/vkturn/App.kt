package io.github.oldatstec.vkturn

import android.app.Application

/** Catches what would otherwise end as "the app just closes" (CrashLog). */
class App : Application() {
    override fun onCreate() {
        super.onCreate()
        Tunnel.attachLogFile(this)
        val system = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { t, e ->
            CrashLog.writeException(this, t, e)
            system?.uncaughtException(t, e)
        }
    }
}
