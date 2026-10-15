package io.github.oldatstec.vkturn

import android.Manifest
import android.app.Activity
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.os.Bundle
import android.text.Editable
import android.text.TextWatcher
import android.view.View
import android.view.ViewGroup
import android.widget.BaseAdapter
import android.widget.CheckBox
import android.widget.EditText
import android.widget.ImageView
import android.widget.ListView
import android.widget.TextView
import kotlin.concurrent.thread

/**
 * The split tunnel's list: which apps go into the tunnel (or past it, by the
 * mode on the main screen). Apps without the INTERNET permission are not
 * shown — they have nothing to route. "System" are the ones without an icon
 * in the launcher: a preinstalled browser or YouTube is a system app by its
 * flags, yet the first thing one routes. Selected apps come first.
 */
class AppsActivity : Activity() {
    private class App(val pkg: String, val label: String, val system: Boolean, val info: ApplicationInfo)

    private var all = emptyList<App>()
    private var shown = emptyList<App>()
    private val selected = mutableSetOf<String>()
    private lateinit var adapter: Adapter

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_apps)
        selected += Settings.load(this).apps
        adapter = Adapter()
        val list = findViewById<ListView>(R.id.list)
        list.adapter = adapter
        list.emptyView = findViewById(R.id.empty)
        list.setOnItemClickListener { _, _, pos, _ ->
            val a = shown[pos]
            if (!selected.remove(a.pkg)) selected += a.pkg
            Settings.saveApps(this, selected.toSet())
            adapter.notifyDataSetChanged()
        }
        findViewById<EditText>(R.id.search).addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
            override fun onTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
            override fun afterTextChanged(s: Editable?) = filter()
        })
        findViewById<CheckBox>(R.id.show_system).setOnCheckedChangeListener { _, _ -> filter() }
        load()
    }

    private fun load() = thread {
        val pm = packageManager
        val apps = pm.getInstalledPackages(PackageManager.GET_PERMISSIONS)
            .filter { it.packageName != packageName }
            .filter { it.requestedPermissions?.contains(Manifest.permission.INTERNET) == true }
            .mapNotNull { pi ->
                val ai = pi.applicationInfo ?: return@mapNotNull null
                App(
                    pi.packageName,
                    ai.loadLabel(pm).toString(),
                    ai.flags and (ApplicationInfo.FLAG_SYSTEM or ApplicationInfo.FLAG_UPDATED_SYSTEM_APP) != 0 &&
                        pm.getLaunchIntentForPackage(pi.packageName) == null,
                    ai,
                )
            }
            .sortedWith(compareBy({ it.pkg !in selected }, { it.label.lowercase() }))
        runOnUiThread {
            findViewById<TextView>(R.id.empty).setText(R.string.apps_none)
            all = apps
            filter()
        }
    }

    private fun filter() {
        val q = findViewById<EditText>(R.id.search).text.toString().trim().lowercase()
        val sys = findViewById<CheckBox>(R.id.show_system).isChecked
        shown = all.filter { a ->
            (sys || !a.system || a.pkg in selected) &&
                (q.isEmpty() || q in a.label.lowercase() || q in a.pkg)
        }
        adapter.notifyDataSetChanged()
    }

    private inner class Adapter : BaseAdapter() {
        override fun getCount() = shown.size
        override fun getItem(position: Int) = shown[position]
        override fun getItemId(position: Int) = position.toLong()

        override fun getView(position: Int, convertView: View?, parent: ViewGroup): View {
            val v = convertView ?: layoutInflater.inflate(R.layout.item_app, parent, false)
            val a = shown[position]
            v.findViewById<TextView>(R.id.label).text = a.label
            v.findViewById<TextView>(R.id.pkg).text = a.pkg
            v.findViewById<CheckBox>(R.id.check).isChecked = a.pkg in selected
            v.findViewById<ImageView>(R.id.icon).setImageDrawable(a.info.loadIcon(packageManager))
            return v
        }
    }
}
